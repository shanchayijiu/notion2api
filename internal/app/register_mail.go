package app

// register_mail.go — 纯 Go 临时邮箱源（注册链 Step1/5），替代 Python temp_mail/adguard_tempmail：
//   - mail.tm：全纯 HTTP（创建账号 + JWT + 轮询收信），不依赖浏览器。
//   - adguard（tempmail.adguard.com）：建邮箱需浏览器过 capjs，Go 端不支持新建；
//     仅支持"已存在 mailbox cookie 文件"（register/mailboxes/<local>_at_<domain>.json）
//     的纯 HTTP 收信轮询 —— 与 Python 侧跨进程恢复逻辑一致。
// 验证码抽取规则对齐 adguard_tempmail._extract_code：3+3 分隔 / 独立 6 位数字。

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	reCodeDash  = regexp.MustCompile(`(?m)[^0-9]?([0-9]{3})-([0-9]{3})[^0-9]?`)
	reCodeGap   = regexp.MustCompile(`(?m)[^0-9]?([0-9]{3})\s([0-9]{3})[^0-9]?`)
	reCodePlain = regexp.MustCompile(`[^0-9]([0-9]{6})[^0-9]`)
	reHTMLTags  = regexp.MustCompile(`<[^>]+>`)
	// 魔链变体：老账号常发 "Sign in with Magic Link"（正文无 6 位数字），
	// 临时密码藏在 loginwithemail?...&password=<token>（2026-08-31 实测两种模板轮换投放）
	reMagicLinkPassword = regexp.MustCompile(`loginwithemail[^"<>\s]*?password=([A-Za-z0-9_-]{4,32})`)
)

// extractNotionCode — 从邮件正文提取 6 位验证码（对齐 Python _extract_code）
func extractNotionCode(plain string) string {
	if m := reCodeDash.FindStringSubmatch(plain); m != nil {
		return m[1] + m[2]
	}
	if m := reCodeGap.FindStringSubmatch(plain); m != nil {
		return m[1] + m[2]
	}
	cleaned := reCodeGap.ReplaceAllString(plain, " ")
	cleaned = reCodeDash.ReplaceAllString(cleaned, " ")
	if m := reCodePlain.FindStringSubmatch(cleaned); m != nil {
		return m[1]
	}
	// 兜底：全文去掉空白/横线后找独立 6 位
	squashed := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, plain)
	if m := regexp.MustCompile(`^([0-9]{6})$|[^0-9]([0-9]{6})[^0-9]`).FindStringSubmatch(squashed); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				return g
			}
		}
	}
	// 魔链变体兜底：老账号常收到 "Sign in with Magic Link"（正文无 6 位数字），
	// 临时密码在 loginwithemail?...&password=<token>（2026-08-31 实测两种模板轮换投放）
	if m := reMagicLinkPassword.FindStringSubmatch(plain); m != nil {
		return m[1]
	}
	return ""
}

func stripHTML(s string) string {
	return html.UnescapeString(reHTMLTags.ReplaceAllString(s, " "))
}

// ── mail.tm 提供器 ─────────────────────────────────────────────────────────

const mailTmBase = "https://api.mail.tm"

// mailTmAccount — mail.tm 账号态（token 轮询收信用）
type mailTmAccount struct {
	Address string
	Token   string
	HTTP    *http.Client
}

// mailTmGenerate — 建 mail.tm 临时邮箱（GET /domains 随机挑活跃域 → POST /accounts → POST /token）
func mailTmGenerate(ctx context.Context, proxy string) (mailTmAccount, error) {
	hc, err := newRegisterPlainClient(proxy)
	if err != nil {
		return mailTmAccount{}, fmt.Errorf("mailtm client: %w", err)
	}
	hc.Timeout = 30 * time.Second

	raw, err := registerHTTPGet(ctx, hc, mailTmBase+"/domains?page=1")
	if err != nil {
		return mailTmAccount{}, fmt.Errorf("mailtm domains: %w", err)
	}
	var members []mailTmDomain
	for _, m := range decodeHydraOrList(raw) {
		if d, ok := m["domain"].(string); ok {
			active, _ := m["isActive"].(bool)
			members = append(members, mailTmDomain{Domain: d, IsActive: active})
		}
	}
	domains := make([]string, 0, len(members))
	for _, d := range members {
		if d.IsActive && strings.TrimSpace(d.Domain) != "" {
			domains = append(domains, d.Domain)
		}
	}
	if len(domains) == 0 {
		return mailTmAccount{}, fmt.Errorf("mailtm: no active domain")
	}
	domain := domains[rand.Intn(len(domains))]
	address := randomMailLocalPart() + "@" + domain
	password := "N2a" + randomToken(12)

	body := map[string]string{"address": address, "password": password}
	if _, err := registerHTTPPostJSON(ctx, hc, mailTmBase+"/accounts", body); err != nil {
		return mailTmAccount{}, fmt.Errorf("mailtm create account %s: %w", address, err)
	}
	tok, err := registerHTTPPostJSON(ctx, hc, mailTmBase+"/token", body)
	if err != nil {
		return mailTmAccount{}, fmt.Errorf("mailtm token %s: %w", address, err)
	}
	var tj struct {
		Token string `json:"token"`
	}
	if uerr := json.Unmarshal(tok, &tj); uerr != nil || strings.TrimSpace(tj.Token) == "" {
		return mailTmAccount{}, fmt.Errorf("mailtm token parse: %v body=%s", uerr, truncateBytes(tok, 200))
	}
	return mailTmAccount{Address: address, Token: tj.Token, HTTP: hc}, nil
}

// mailTmWaitCode — 轮询收件箱直到 Notion 验证码（超时 180s / 间隔 4s，对齐 Python）。
// 只看 notBefore 之后到达的邮件（复用邮箱时排除历史 Notion 验证码）。
func mailTmWaitCode(ctx context.Context, acc mailTmAccount, notBefore time.Time) (string, error) {
	deadline := time.Now().Add(180 * time.Second)
	seen := map[string]bool{}
	polls := 0
	for time.Now().Before(deadline) {
		polls++
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, mailTmBase+"/messages?page=1", nil)
		req.Header.Set("Authorization", "Bearer "+acc.Token)
		req.Header.Set("Accept", "application/ld+json")
		resp, err := acc.HTTP.Do(req)
		if err != nil {
			log.Printf("[register_mail] mailtm poll #%d mailbox=%s error: %v", polls, acc.Address, err)
		}
		if err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				for _, m := range decodeHydraOrList(raw) {
					id, _ := m["id"].(string)
					subject, _ := m["subject"].(string)
					text, _ := m["text"].(string)
					if id == "" || seen[id] {
						continue
					}
					seen[id] = true
					if ts, ok := m["createdAt"].(string); ok {
						if rt := parseRFC3339Loose(ts); !rt.IsZero() && rt.Before(notBefore) {
							continue // 历史邮件（复用邮箱），跳过
						}
					}
					// hay 含原始 HTML：魔链 email 的 password= 在 <a href> 属性里
					hay := subject + "\n" + stripHTML(text) + "\n" + text
					if !strings.Contains(strings.ToLower(hay), "notion") {
						continue
					}
					if code := extractNotionCode(hay); code != "" {
						return code, nil
					}
					if code := extractNotionCode(text); code != "" {
						return code, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(4 * time.Second):
		}
	}
	return "", fmt.Errorf("mailtm: no notion verification code within timeout")
}

func randomMailLocalPart() string {
	return "n2a" + randomToken(10)
}

func randomToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

// ── AdGuard 提供器（仅复用已建 mailbox，纯 HTTP 收信）───────────────────────

type adguardMailbox struct {
	Address string
	Cookies map[string]string
}

// adguardPickMailbox — 从 mailboxes 目录挑一个"本机还没注册成功账号"的 mailbox。
// 文件名 <local>_at_<domain>.json → 地址 <local>@<domain>；排除 accounts/detail/<email> 已存在者。
func adguardPickMailbox(mailboxesDir string, detailRoot string) (adguardMailbox, error) {
	entries, err := os.ReadDir(mailboxesDir)
	if err != nil {
		return adguardMailbox{}, fmt.Errorf("adguard mailboxes dir %s: %w", mailboxesDir, err)
	}
	var candidates []adguardMailbox
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		stem := strings.TrimSuffix(name, ".json")
		at := strings.LastIndex(stem, "_at_")
		if at <= 0 {
			continue
		}
		address := stem[:at] + "@" + stem[at+4:]
		// 只跳过注册成功的 mailbox（probe.json 存在才算销号成功；
		// account.json 在失败路径也会落盘，不能据此永久报废还能用的 mailbox）
		if fileExists(filepath.Join(detailRoot, strings.ReplaceAll(address, "/", "_"), "probe.json")) {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(mailboxesDir, name))
		if rerr != nil {
			continue
		}
		var mj struct {
			Cookies map[string]string `json:"cookies"`
		}
		if uerr := json.Unmarshal(raw, &mj); uerr != nil || len(mj.Cookies) == 0 {
			continue
		}
		candidates = append(candidates, adguardMailbox{Address: address, Cookies: mj.Cookies})
	}
	if len(candidates) == 0 {
		return adguardMailbox{}, fmt.Errorf("adguard: no unused mailbox under %s (mailbox creation needs browser; not supported in Go)", mailboxesDir)
	}
	return candidates[rand.Intn(len(candidates))], nil
}

// adguardWaitCode — 轮询 AdGuard 收信（GET /messages?since_message_id=0 + /message/<id>，
// cookie: user+mailbox；超时 840s / 间隔 20s——实测 Notion 邮件延迟可到 ~11min）。
func adguardWaitCode(ctx context.Context, proxy string, mb adguardMailbox, notBefore time.Time) (string, error) {
	const apiHome = "https://tempmail.adguard.com"
	hc, err := newRegisterPlainClient(proxy)
	if err != nil {
		return "", err
	}
	hc.Timeout = 20 * time.Second
	jar := hc.Jar
	if jar != nil {
		u, perr := url.Parse(apiHome)
		if perr == nil {
			cks := []*http.Cookie{}
			for k, v := range mb.Cookies {
				cks = append(cks, &http.Cookie{Name: k, Value: v, Path: "/"})
			}
			jar.SetCookies(u, cks)
		}
	}
	// 实测 Notion 验证码邮件延迟可达 ~11min（同 IP 密集发送时更慢）：
	// 420s 必然超时失败 → 放宽到 14min（cs my 注册总超 1200s 仍够走完整链）
	deadline := time.Now().Add(840 * time.Second)
	seen := map[string]bool{}
	polls := 0
	for time.Now().Before(deadline) {
		polls++
		raw, err := registerHTTPGet(ctx, hc, apiHome+"/messages?since_message_id=0")
		if err != nil {
			log.Printf("[register_mail] adguard poll #%d mailbox=%s error: %v", polls, mb.Address, err)
		}
		if err == nil {
			var mj struct {
				Emails []struct {
					MessageID string `json:"message_id"`
					Subject   string `json:"subject"`
					Snippet   string `json:"snippet"`
					TimeAdded string `json:"time_added"`
				} `json:"emails"`
			}
			if uerr := json.Unmarshal(raw, &mj); uerr == nil {
				for _, e := range mj.Emails {
					if e.MessageID == "" || seen[e.MessageID] {
						continue
					}
					seen[e.MessageID] = true
					if rt := parseRFC3339Loose(e.TimeAdded); !rt.IsZero() && rt.Before(notBefore) {
						continue // 历史邮件（复用邮箱），跳过
					}
					content := e.Snippet
					if full, ferr := registerHTTPGet(ctx, hc, apiHome+"/message/"+e.MessageID); ferr == nil {
						var fj struct {
							ContentHTML string `json:"content_html"`
						}
						if uerr := json.Unmarshal(full, &fj); uerr == nil && fj.ContentHTML != "" {
							content = fj.ContentHTML
						}
					}
					plain := stripHTML(content)
					hay := e.Subject + "\n" + plain + "\n" + content
					if !strings.Contains(strings.ToLower(hay), "notion") {
						continue
					}
					if code := extractNotionCode(hay); code != "" {
						return code, nil
					}
				}
			}
		}
		log.Printf("[register_mail] adguard poll #%d mailbox=%s seen_msgs=%d", polls, mb.Address, len(seen))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Second):
		}
	}
	return "", fmt.Errorf("adguard: no notion verification code within timeout")
}

// ── mail.tm 响应解码（hydra ld+json 与 plain JSON 两种形态都出现）───────────

type mailTmDomain struct {
	Domain   string
	IsActive bool
}

// decodeHydraOrList — 兼容 {"hydra:member":[...]} 与直接 [...] 两种响应
func decodeHydraOrList(raw []byte) []map[string]any {
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var env struct {
		Members []map[string]any `json:"hydra:member"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		return env.Members
	}
	return nil
}

// newRegisterPlainClient — 邮服务的纯 net/http 客户端（不走 surf 指纹：
// surf 强制浏览器 Accept 头会让 mail.tm 内容协商成 XML；mail.tm/adguard 均无 Chrome 指纹要求）。
// 仅代理转发 + cookie jar，对齐 Python curl_cffi 在此处的行为。
func newRegisterPlainClient(proxy string) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
	}
	if strings.TrimSpace(proxy) != "" {
		u, perr := url.Parse(proxy)
		if perr != nil {
			return nil, fmt.Errorf("parse proxy %s: %w", proxy, perr)
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second}, nil
}

// parseRFC3339Loose — 宽松解析 RFC3339 / "2006-01-02 15:04:05" 两种格式
func parseRFC3339Loose(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ── guerrillamail 提供器（2026-08-31 新接入）─────────────────────────────────
// 全纯 HTTP ajax API，无鉴权；无限地址 → 号池扩容到 100+ 的邮源。
// 实测 Notion 接受域名：guerrillamailblock.com / spam4.me / guerrillamail.info /
// guerrillamail.net / guerrillamail.de（sharklasers/grr.la 被拒）。
// 复用契约：get_email_address → 地址+sid_token；set_email_user 可固定用户名；
// check_email 列信；fetch_email 取正文。sid_token 长有效，收件箱随地址留存。
// 用户名字符集：纯小写字母数字下划线（guerrillamail 限制）。

const guerrillaApiBase = "https://api.guerrillamail.com/ajax.php"

// guerrillaAcceptedDomains — 实测 Notion 接受的 guerrillamail 族域名（轮换防单域聚类）
var guerrillaAcceptedDomains = []string{
	"guerrillamailblock.com", "spam4.me", "guerrillamail.info", "guerrillamail.net", "guerrillamail.de",
}

type guerrillaMailbox struct {
	Address  string `json:"address"`
	SidToken string `json:"sid_token"`
}

func guerrillaCall(ctx context.Context, proxy string, f string, params url.Values, out any) error {
	hc, err := newRegisterPlainClient(proxy)
	if err != nil {
		return err
	}
	defer hc.CloseIdleConnections()
	_ = hc
	q := url.Values{}
	q.Set("f", f)
	q.Set("ip", fmt.Sprintf("%d.%d.%d.%d", 64+rand.Intn(60), rand.Intn(255), rand.Intn(255), rand.Intn(254)+1))
	q.Set("agent", "DSH_register")
	for k, vs := range params {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", guerrillaApiBase+"?"+q.Encode(), nil)
	req.Header.Set("User-Agent", registerUA)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("guerrillamail %s: status=%d body=%s", f, resp.StatusCode, truncateBytes(raw, 200))
	}
	return json.Unmarshal(raw, out)
}

// guerrillaNewMailbox — 新建邮箱：get_email_address（拿 sid）→ set_email_user（随机用户名+轮换域名）
func guerrillaNewMailbox(ctx context.Context, proxy string) (guerrillaMailbox, error) {
	var r1 struct {
		EmailAddr string `json:"email_addr"`
		SidToken  string `json:"sid_token"`
	}
	if err := guerrillaCall(ctx, proxy, "get_email_address", nil, &r1); err != nil {
		return guerrillaMailbox{}, err
	}
	if r1.SidToken == "" {
		return guerrillaMailbox{}, fmt.Errorf("guerrillamail get_email_address: no sid_token")
	}
	// 固定邮箱名：regex 允许的 [a-z0-9_]
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	name := "n2a" + string(b)
	var r2 struct {
		EmailAddr string `json:"email_addr"`
	}
	p := url.Values{"email_user": {name}, "sid_token": {r1.SidToken}}
	// ajax API set_email_user 只改用户名不改域；域轮换取决于账号批次时的 get_email_address 默认域。
	// guerrillaAcceptedDomains 中均实测 Notion 接受；后续可在邮箱批次级均衡分布。
	if err := guerrillaCall(ctx, proxy, "set_email_user", p, &r2); err != nil {
		// set_email_user 失败不致命：用自动分配的地址
		if r1.EmailAddr != "" {
			return guerrillaMailbox{Address: r1.EmailAddr, SidToken: r1.SidToken}, nil
		}
		return guerrillaMailbox{}, err
	}
	addr := r2.EmailAddr
	if addr == "" {
		addr = r1.EmailAddr
	}
	if addr == "" {
		return guerrillaMailbox{}, fmt.Errorf("guerrillamail: empty address after set_email_user")
	}
	log.Printf("[register_mail] guerrillamail new mailbox: %s", addr)
	return guerrillaMailbox{Address: addr, SidToken: r1.SidToken}, nil
}

// guerrillaWaitCode — 轮询 check_email → fetch_email 提取验证码（复用 extractNotionCode）
func guerrillaWaitCode(ctx context.Context, proxy string, mb guerrillaMailbox, notBefore time.Time) (string, error) {
	deadline := time.Now().Add(840 * time.Second)
	seen := map[int64]bool{}
	polls := 0
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		polls++
		var list struct {
			List []struct {
				MailID        int64  `json:"mail_id"`
				MailFrom      string `json:"mail_from"`
				MailSubject   string `json:"mail_subject"`
				MailPreview   string `json:"mail_excerpt"`
				MailTimestamp string `json:"mail_timestamp"`
				MailDate      string `json:"mail_date"`
			} `json:"list"`
		}
		err := guerrillaCall(ctx, proxy, "check_email", url.Values{"sid_token": {mb.SidToken}, "seq": {"0"}}, &list)
		if err != nil {
			log.Printf("[register_mail] guerrillamail poll #%d mailbox=%s error: %v", polls, mb.Address, err)
		} else {
			log.Printf("[register_mail] guerrillamail poll #%d mailbox=%s seen_msgs=%d", polls, mb.Address, len(list.List))
			for _, m := range list.List {
				if seen[m.MailID] {
					continue
				}
				seen[m.MailID] = true
				if !strings.Contains(strings.ToLower(m.MailFrom), "notion") {
					continue
				}
				// 抓全文：mail_body 是 HTML（magic-link 变体密码在 href 里）
				var full struct {
					MailBody string `json:"mail_body"`
					MailDate string `json:"mail_date"`
				}
				if err := guerrillaCall(ctx, proxy, "fetch_email", url.Values{
					"sid_token": {mb.SidToken}, "email_id": {fmt.Sprintf("%d", m.MailID)},
				}, &full); err != nil {
					continue
				}
				body := full.MailBody
				hay := m.MailSubject + "\n" + stripHTML(body) + "\n" + body
				if code := extractNotionCode(hay); code != "" {
					return code, nil
				}
			}
		}
		if !sleepCtx(ctx, 15*time.Second) {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("guerrillamail code wait timeout after 840s")
}
