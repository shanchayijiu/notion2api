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
	"io"
	"fmt"
	"html"
	"math/rand"
	"net/http"
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
	hc, err := newSurfStdClient(proxy)
	if err != nil {
		return mailTmAccount{}, fmt.Errorf("mailtm client: %w", err)
	}
	hc.Timeout = 30 * time.Second

	raw, err := registerHTTPGet(ctx, hc, mailTmBase+"/domains?page=1")
	if err != nil {
		return mailTmAccount{}, fmt.Errorf("mailtm domains: %w", err)
	}
	var dj struct {
		Members []struct {
			Domain   string `json:"domain"`
			IsActive bool   `json:"isActive"`
		} `json:"hydra:member"`
	}
	if uerr := json.Unmarshal(raw, &dj); uerr != nil {
		return mailTmAccount{}, fmt.Errorf("mailtm domains parse: %w body=%s", uerr, truncateBytes(raw, 200))
	}
	domains := make([]string, 0, len(dj.Members))
	for _, d := range dj.Members {
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

// mailTmWaitCode — 轮询收件箱直到 Notion 验证码（超时 180s / 间隔 4s，对齐 Python）
func mailTmWaitCode(ctx context.Context, acc mailTmAccount) (string, error) {
	deadline := time.Now().Add(180 * time.Second)
	seen := map[string]bool{}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, mailTmBase+"/messages?page=1", nil)
		req.Header.Set("Authorization", "Bearer "+acc.Token)
		req.Header.Set("Accept", "application/ld+json")
		resp, err := acc.HTTP.Do(req)
		if err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var mj struct {
					Members []struct {
						ID      string `json:"id"`
						Subject string `json:"subject"`
						Text    string `json:"text"`
					} `json:"hydra:member"`
				}
				if uerr := json.Unmarshal(raw, &mj); uerr == nil {
					for _, m := range mj.Members {
						if m.ID == "" || seen[m.ID] {
							continue
						}
						seen[m.ID] = true
						hay := m.Subject + "\n" + stripHTML(m.Text)
						if !strings.Contains(strings.ToLower(hay), "notion") {
							continue
						}
						code := extractNotionCode(hay)
						if code == "" {
							code = extractNotionCode(m.Text)
						}
						if code != "" {
							return code, nil
						}
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
	Address  string
	Cookies  map[string]string
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
		// 跳过已产出账号的 mailbox（detail dir 已存在）
		if fileExists(filepath.Join(detailRoot, strings.ReplaceAll(address, "/", "_"), "account.json")) {
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
// cookie: user+mailbox；对齐 Python：超时 420s / 间隔 20s）。
func adguardWaitCode(ctx context.Context, proxy string, mb adguardMailbox) (string, error) {
	const apiHome = "https://tempmail.adguard.com"
	hc, err := newSurfStdClient(proxy)
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
	deadline := time.Now().Add(420 * time.Second)
	seen := map[string]bool{}
	for time.Now().Before(deadline) {
		raw, err := registerHTTPGet(ctx, hc, apiHome+"/messages?since_message_id=0")
		if err == nil {
			var mj struct {
				Emails []struct {
					MessageID string `json:"message_id"`
					Subject   string `json:"subject"`
					Snippet   string `json:"snippet"`
				} `json:"emails"`
			}
			if uerr := json.Unmarshal(raw, &mj); uerr == nil {
				for _, e := range mj.Emails {
					if e.MessageID == "" || seen[e.MessageID] {
						continue
					}
					seen[e.MessageID] = true
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
					hay := e.Subject + "\n" + plain
					if !strings.Contains(strings.ToLower(hay), "notion") {
						continue
					}
					if code := extractNotionCode(hay); code != "" {
						return code, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Second):
		}
	}
	return "", fmt.Errorf("adguard: no notion verification code within timeout")
}
