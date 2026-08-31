package app

// register_chain.go — Notion 注册链纯 Go 移植（对齐 notion_register_proto.py 9 步）。
// 链路：tempmail 邮箱 → GET /signup 种 cookie → getAppConfig → getLoginOptions →
// sendTemporaryPassword（invalid_email_domain ~5 次换邮箱重试）→ 轮询 6 位验证码 →
// loginWithEmail（拿 token_v2）→ getLifecycleUserProfile/getSpacesInitial →
// getJoinableSpaces/selfJoinSpaceByDomain 或 createspace(personal) → getAvailableModels →
// 落盘 probe.json / account.json / trace + 桶 accounts.txt。
// 纯 HTTP（surf Chrome 指纹 + 标准 cookiejar），Docker 容器内原生可跑，无 python/playwright。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	registerNotionAppHome = "https://app.notion.com"
	registerNotionWWW     = "https://www.notion.so"
	registerSignupURL     = "https://www.notion.so/signup"
	registerAPIApp        = "https://app.notion.com/api/v3"

	registerUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	registerSecChUa = `"Chromium";v="147", "Not.A/Brand";v="8"`

	registerClientVersionDefault = "23.13.0.20260811.1552"
)

var reNotionDataVersion = regexp.MustCompile(`data-notion-version="([0-9._-]+)"`)
var reFivePartVersion = regexp.MustCompile(`^23\.[0-9]+\.[0-9]+\.(19|20)[0-9]{6}\.[0-9]+$`)

const (
	registerMaxEmailAttempts     = 5
	registerBarrelSize           = 50
	registerAllowSpaceless       = true
)

// registerGoOptions — Go 注册链入参（由 register_provider 从配置汇总）
type registerGoOptions struct {
	Proxy        string
	Password     string
	MailProvider string // "mailtm"（默认）| "adguard"
	SpaceMode      string // "invite"（默认，被邀优先）| "personal"
	OutputRoot     string // register 根：accounts/ logs/ mailboxes/ 都在其下
	OutlookBaseURL string // mail_provider=outlook：outlook-mail 容器基址
	OutlookPassword string // mail_provider=outlook：outlook-mail web 登录密码
}

// registerGoResult — 注册链产物摘要
type registerGoResult struct {
	Email       string
	UserID      string
	SpaceID     string
	AccountDir  string
	ProbePath   string
	Barrel      string
	BarrelSeq   string
	ClientVer   string
	MailboxUsed string
}

// registerTrace — 轻量 jsonl 轨迹
type registerTrace struct {
	mu   sync.Mutex
	file *os.File
}

func newRegisterTrace(path string) *registerTrace {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		return &registerTrace{}
	}
	return &registerTrace{file: f}
}

func (t *registerTrace) log(fields map[string]any) {
	if t == nil || t.file == nil {
		return
	}
	rec := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano)}
	for k, v := range fields {
		rec[k] = v
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.file.Write(append(raw, '\n'))
}

func (t *registerTrace) close() {
	if t == nil || t.file == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.file.Close()
	t.file = nil
}

// ── 底层 HTTP 助手（shared with register_mail.go）──────────────────────────

func registerHTTPGet(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", registerUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", url)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s -> %d: %s", url, resp.StatusCode, truncateBytes(raw, 200))
	}
	return raw, nil
}

func registerHTTPPostJSON(ctx context.Context, hc *http.Client, url string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", registerUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s -> %d: %s", url, resp.StatusCode, truncateBytes(out, 300))
	}
	return out, nil
}

// notionAPIHeaders — Notion v3 标准头（对齐 Python HEADERS_BASE）
func notionAPIHeaders(clientVersion string) map[string]string {
	return map[string]string{
		"User-Agent":                 registerUA,
		"Accept":                     "application/json, text/plain, */*",
		"Accept-Language":            "en-US,en;q=0.9",
		"Content-Type":               "application/json",
		"sec-ch-ua":                  registerSecChUa,
		"sec-ch-ua-mobile":           "?0",
		"sec-ch-ua-platform":         `"Windows"`,
		"sec-fetch-dest":             "empty",
		"sec-fetch-mode":             "cors",
		"sec-fetch-site":             "same-origin",
		"notion-audit-log-platform":  "web",
		"notion-client-version":      clientVersion,
	}
}

// notionPost — Notion v3 POST（带完整头 + Referer/Origin），返回 body 与 status
func notionPost(ctx context.Context, hc *http.Client, path string, body any, clientVersion string, referer string, extraHeaders map[string]string) ([]byte, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registerAPIApp+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	for k, v := range notionAPIHeaders(clientVersion) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Origin", registerNotionAppHome)
	referer = strings.TrimSpace(referer)
	if referer == "" {
		referer = registerNotionAppHome + "/"
	}
	req.Header.Set("Referer", referer)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return out, resp.StatusCode, nil
}

func decodeJSON(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// sessionCookiesFull — 读 jar 里的 cookie（对齐 Python s.cookies.jar）
func sessionCookiesFull(hc *http.Client) []map[string]string {
	if hc.Jar == nil {
		return nil
	}
	var all []*http.Cookie
	for _, host := range []string{registerNotionAppHome, registerNotionWWW} {
		u, err := url.Parse(host)
		if err != nil {
			continue
		}
		for _, c := range hc.Jar.Cookies(u) {
			dup := false
			for _, e := range all {
				if e.Name == c.Name {
					dup = true
					break
				}
			}
			if !dup {
				all = append(all, c)
			}
		}
	}
	out := make([]map[string]string, 0, len(all))
	for _, c := range all {
		out = append(out, map[string]string{"name": c.Name, "value": c.Value})
	}
	return out
}

// sessionCookieValue — 从 jar 取单个 cookie 值
func sessionCookieValue(hc *http.Client, name string) string {
	for _, c := range sessionCookiesFull(hc) {
		if c["name"] == name {
			return c["value"]
		}
	}
	return ""
}

// ── 注册链主函数（对齐 notion_register_proto.register_one）──────────────────

// registerOneGo — 纯 Go 注册一个 Notion 账号。返回结果 + 落盘 probe.json/account.json/trace/桶。
func registerOneGo(ctx context.Context, opts registerGoOptions) (registerGoResult, error) {
	mailProvider := strings.ToLower(strings.TrimSpace(opts.MailProvider))
	if mailProvider == "" {
		mailProvider = "mailtm"
	}
	spaceMode := strings.ToLower(strings.TrimSpace(opts.SpaceMode))
	if spaceMode == "" {
		spaceMode = "invite"
	}
	root := strings.TrimSpace(opts.OutputRoot)
	if root == "" {
		root = "."
	}
	detailRoot := filepath.Join(root, "accounts", "detail")
	logsDir := filepath.Join(root, "logs")
	mailboxesDir := filepath.Join(root, "mailboxes")
	_ = os.MkdirAll(detailRoot, 0o755)

	password := strings.TrimSpace(opts.Password)
	if password == "" {
		password = "TempPwd2026!" + randomToken(6)
	}
	deviceID := randomUUID()
	browserID := deviceID // Notion 实测两个同值

	trace := newRegisterTrace(filepath.Join(logsDir, "last_trace.jsonl"))
	defer trace.close()
	trace.log(map[string]any{"phase": "start", "proxy": opts.Proxy, "mail_provider": mailProvider, "space_mode": spaceMode})

	var (
		email       string
		mailAcc     mailTmAccount
		adgMB       adguardMailbox
		grrMB       guerrillaMailbox
		outlookPoolH *outlookPool
		hc          *http.Client
		loToken     string
		csrfState   string
		cv          = registerClientVersionDefault
		country     string
		accountDir  string
		triedDirs   []string
		badDomains  = map[string]bool{}
	)

	for attempt := 0; attempt < registerMaxEmailAttempts; attempt++ {
		trace.log(map[string]any{"phase": "email_retry", "attempt": attempt, "bad_domains": keysOf(badDomains)})

		// Step 1: 临时邮箱
		if mailProvider == "outlook" {
			if outlookPoolH == nil {
				outlookPoolH = newOutlookPool(strings.TrimSpace(opts.OutlookBaseURL), strings.TrimSpace(opts.OutlookPassword), opts.Proxy)
			}
			accs, lerr := outlookPoolH.listAccounts(ctx)
			if lerr != nil {
				trace.log(map[string]any{"step": 1, "action": "mail_list_fail", "err": lerr.Error(), "attempt": attempt})
				return registerGoResult{}, lerr
			}
			acc, perr := outlookPickAddress(accs, detailRoot)
			if perr != nil {
				return registerGoResult{}, perr
			}
			email = strings.ToLower(strings.TrimSpace(acc.Email))
			trace.log(map[string]any{"step": 1, "action": "outlook_mailbox_selected", "email": email})
		} else if mailProvider == "guerrillamail" || mailProvider == "guerrilla" {
			mb, err := guerrillaNewMailbox(ctx, opts.Proxy)
			if err != nil {
				trace.log(map[string]any{"step": 1, "action": "mail_gen_fail", "err": err.Error(), "attempt": attempt, "provider": "guerrillamail"})
				if attempt == registerMaxEmailAttempts-1 {
					return registerGoResult{}, err
				}
				if !sleepCtx(ctx, 2*time.Second) {
					return registerGoResult{}, ctx.Err()
				}
				continue
			}
			grrMB = mb
			email = mb.Address
		} else if mailProvider == "adguard" {
			mb, err := adguardPickMailbox(mailboxesDir, detailRoot)
			if err != nil {
				trace.log(map[string]any{"step": 1, "action": "mail_gen_fail", "err": err.Error(), "attempt": attempt})
				return registerGoResult{}, err
			}
			adgMB = mb
			email = mb.Address
		} else {
			acc, err := mailTmGenerate(ctx, opts.Proxy)
			if err != nil {
				trace.log(map[string]any{"step": 1, "action": "mail_gen_fail", "err": err.Error(), "attempt": attempt})
				if attempt == registerMaxEmailAttempts-1 {
					return registerGoResult{}, err
				}
				if !sleepCtx(ctx, 2*time.Second) {
					return registerGoResult{}, ctx.Err()
				}
				continue
			}
			mailAcc = acc
			email = acc.Address
		}
		domain := ""
		if at := strings.Index(email, "@"); at >= 0 {
			domain = strings.ToLower(email[at+1:])
		}
		accountDir = filepath.Join(detailRoot, strings.ReplaceAll(email, "/", "_"))
		_ = os.MkdirAll(accountDir, 0o755)
		triedDirs = append(triedDirs, accountDir)
		trace.log(map[string]any{"step": 1, "action": "mail_ok", "email": email, "domain": domain, "attempt": attempt})

		// 重开 session（避免上一轮 invalid_email_domain 的 cookie 干扰）
		var err error
		hc, err = newSurfStdClient(opts.Proxy)
		if err != nil {
			return registerGoResult{}, fmt.Errorf("http client: %w", err)
		}
		hc.Timeout = 30 * time.Second

		// Step 2: GET /signup 种 cookie
		{
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, registerSignupURL, nil)
			req.Header.Set("User-Agent", registerUA)
			req.Header.Set("Accept-Language", "en-US,en;q=0.9")
			req.Header.Set("sec-ch-ua", registerSecChUa)
			req.Header.Set("sec-ch-ua-mobile", "?0")
			req.Header.Set("sec-ch-ua-platform", `"Windows"`)
			resp, err := hc.Do(req)
			if err != nil {
				trace.log(map[string]any{"step": 2, "action": "get_signup_fail", "err": err.Error(), "attempt": attempt})
				if attempt == registerMaxEmailAttempts-1 {
					return registerGoResult{}, fmt.Errorf("GET /signup: %w", err)
				}
				if !sleepCtx(ctx, 2*time.Second) {
					return registerGoResult{}, ctx.Err()
				}
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			// 2026-08-31 实录：过期 clientVersion 会让上游把所有新域名报
			// UserValidationError(email_unreachable)。signup HTML 里嵌入
			// data-notion-version="<current>"；常作为 getAppConfig 的兜底源。
			// 只采纳五段式版本（23.13.N.YYYYMMDD.HHMM）：四段式被服务端邮件闸口拒，
			// 未来日期也被拒(2026-08-31 矩阵实测)。格式不符回落 proven 常量。
			if m := reNotionDataVersion.FindSubmatch(body); m != nil {
				if v := string(m[1]); reFivePartVersion.MatchString(v) {
					cv = v
				}
			}
			trace.log(map[string]any{"step": 2, "action": "get_signup_resp", "status": resp.StatusCode, "client_version": cv})
		}

		// getAppConfig（失败可降权）
		if raw, st, err := notionPost(ctx, hc, "/getAppConfig", map[string]any{
			"browserId": browserID, "deviceId": deviceID,
			"device": map[string]any{
				"clientVersion": registerClientVersionDefault, "isElectron": false,
				"isMobileNative": false, "isMobileBeta": false, "isMobileBrowser": false,
				"isBrowser": true, "isMobile": false, "isDesktopBrowser": true,
				"isTablet": false, "os": "windows", "browserName": "Chrome", "platform": "Web",
			},
		}, registerClientVersionDefault, registerNotionAppHome+"/signup", nil); err == nil {
			if j := decodeJSON(raw); j != nil {
				if su, ok := j["statsigUser"].(map[string]any); ok {
					if cu, ok := su["custom"].(map[string]any); ok {
						if v, _ := cu["clientVersion"].(string); v != "" {
							cv = v
						}
						if v, _ := cu["country"].(string); v != "" {
							country = v
						}
					}
				}
			}
			trace.log(map[string]any{"step": 2, "action": "get_app_config_resp", "status": st, "country": country, "client_version": cv})
		}

		// Step 3: getLoginOptions
		raw, st, err := notionPost(ctx, hc, "/getLoginOptions", map[string]any{
			"email": email, "requireWorkTypeEmail": false,
		}, cv, registerNotionAppHome+"/signup", nil)
		j := decodeJSON(raw)
		if err == nil && j != nil {
			loToken, _ = j["loginOptionsToken"].(string)
		}
		trace.log(map[string]any{"step": 3, "action": "get_login_options_resp", "status": st, "attempt": attempt, "has_token": loToken != ""})
		if loToken == "" {
			if attempt == registerMaxEmailAttempts-1 {
				writeAccountJSON(accountDir, map[string]any{"email": email, "password": password, "status": "no_login_options_token", "step": 3})
				return registerGoResult{}, fmt.Errorf("getLoginOptions no token: status=%d body=%s", st, truncateBytes(raw, 300))
			}
			if !sleepCtx(ctx, 3*time.Second) {
				return registerGoResult{}, ctx.Err()
			}
			continue
		}

		// Step 4: sendTemporaryPassword
		raw, st, err = notionPost(ctx, hc, "/sendTemporaryPassword", map[string]any{
			"email": email, "disableLoginLink": false, "native": false,
			"isSignup": true, "shouldHidePasscode": false,
			"loginOptionsToken": loToken,
		}, cv, registerNotionAppHome+"/signup", nil)
		j = decodeJSON(raw)
		csrfState = ""
		if err == nil && j != nil {
			csrfState, _ = j["csrfState"].(string)
		}
		trace.log(map[string]any{"step": 4, "action": "send_temp_pwd_resp", "status": st, "attempt": attempt, "has_csrf": csrfState != ""})
		if csrfState == "" {
			cdType, errName := "", ""
			if j != nil {
				errName, _ = j["name"].(string)
				if cd, ok := j["clientData"].(map[string]any); ok {
					cdType, _ = cd["type"].(string)
				}
			}
			trace.log(map[string]any{"step": 4, "action": "no_csrf_state_diag", "client_data_type": cdType, "name": errName, "status": st, "response_short": truncateBytes(raw, 400), "attempt": attempt})
			if cdType == "invalid_email_domain" || errName == "UserValidationError" {
				badDomains[domain] = true
				trace.log(map[string]any{"step": 4, "action": "invalid_email_domain_skip", "domain": domain, "attempt": attempt})
				if attempt == registerMaxEmailAttempts-1 {
					writeAccountJSON(accountDir, map[string]any{"email": email, "password": password, "status": "invalid_email_domain_unrecoverable", "step": 4, "attempt": attempt})
					return registerGoResult{}, fmt.Errorf("%d 次邮箱域都被 Notion invalid_email_domain 拒: %v", registerMaxEmailAttempts, keysOf(badDomains))
				}
				if !sleepCtx(ctx, 2*time.Second) {
					return registerGoResult{}, ctx.Err()
				}
				continue
			}
			writeAccountJSON(accountDir, map[string]any{"email": email, "password": password, "status": "no_csrf_state", "step": 4, "status_code": st})
			return registerGoResult{}, fmt.Errorf("sendTemporaryPassword no csrfState: status=%d body=%s", st, truncateBytes(raw, 300))
		}
		break // 成功，退出邮箱重试循环
	}
	if csrfState == "" {
		return registerGoResult{}, fmt.Errorf("%d 次邮箱重试都没成 sendTemporaryPassword", registerMaxEmailAttempts)
	}

	// Step 5+6：验证码→loginWithEmail 内层重试。
	// 实测 Notion 邮件经过 adguard 延迟 0-11min，而临时密码有效期 ~10min——
	// 撞过期就立即重发重来（同一 csrf/cookie 会话里换新的 csrfState）。
	loginOK := false
	var tokenV2 string
	for loginTry := 0; loginTry < 3 && !loginOK; loginTry++ {
		if loginTry > 0 {
			trace.log(map[string]any{"step": 4, "action": "resend_temp_pwd", "try": loginTry})
			raw, st, err := notionPost(ctx, hc, "/sendTemporaryPassword", map[string]any{
				"email": email, "disableLoginLink": false, "native": false,
				"isSignup": true, "shouldHidePasscode": false,
				"loginOptionsToken": loToken,
			}, cv, registerNotionAppHome+"/signup", nil)
			j2 := decodeJSON(raw)
			csrfState = ""
			if err == nil && j2 != nil {
				csrfState, _ = j2["csrfState"].(string)
			}
			trace.log(map[string]any{"step": 4, "action": "resend_temp_pwd_resp", "status": st, "try": loginTry, "has_csrf": csrfState != "", "err": fmt.Sprint(err)})
			if csrfState == "" {
				continue
			}
			if !sleepCtx(ctx, 3*time.Second) {
				return registerGoResult{}, ctx.Err()
			}
		}

		// Step 5: 拿 6 位验证码 / 魔链密码（notBefore = sendTemporaryPassword 成功时刻）
		notBefore := time.Now().Add(-30 * time.Second)
		trace.log(map[string]any{"step": 5, "action": "wait_for_code_start", "provider": mailProvider, "try": loginTry})
		var code string
		var codeErr error
		if mailProvider == "outlook" {
			code, codeErr = outlookWaitCode(ctx, outlookPoolH, email, notBefore)
		} else if mailProvider == "guerrillamail" || mailProvider == "guerrilla" {
			code, codeErr = guerrillaWaitCode(ctx, opts.Proxy, grrMB, notBefore)
		} else if mailProvider == "adguard" {
			code, codeErr = adguardWaitCode(ctx, opts.Proxy, adgMB, notBefore)
		} else {
			code, codeErr = mailTmWaitCode(ctx, mailAcc, notBefore)
		}
		if codeErr != nil || code == "" {
			trace.log(map[string]any{"step": 5, "action": "no_code", "err": fmt.Sprint(codeErr), "try": loginTry})
			writeAccountJSON(accountDir, map[string]any{"email": email, "password": password, "status": "no_code_in_tempmail", "step": 5})
			return registerGoResult{}, fmt.Errorf("没收到 Notion 邮件或抽不到码: %v", codeErr)
		}
		trace.log(map[string]any{"step": 5, "action": "code_acquired", "try": loginTry})

		// Step 6: loginWithEmail → token_v2 cookie
		ref := registerNotionAppHome + "/loginwithemail"
		raw, st, err := notionPost(ctx, hc, "/loginWithEmail", map[string]any{
			"state": csrfState, "password": code, "email": email,
			"isSignup": true, "appSource": "notion", "loginRouteOrigin": "signup",
		}, cv, ref, nil)
		tokenV2 = sessionCookieValue(hc, "token_v2")
		trace.log(map[string]any{"step": 6, "action": "login_with_email_resp", "status": st, "token_v2_present": tokenV2 != "", "try": loginTry})
		if err == nil && st == 200 && tokenV2 != "" {
			loginOK = true
			break
		}
		// invalid_or_expired_password（邮件晚到超过 10min 有效期）→ 重试；其他错误快速失败
		var dj map[string]any
		_ = json.Unmarshal(raw, &dj)
		cdType := ""
		if dj != nil {
			if cd, ok := dj["clientData"].(map[string]any); ok {
				cdType, _ = cd["type"].(string)
			}
		}
		trace.log(map[string]any{"step": 6, "action": "login_failed_diag", "status": st, "type": cdType, "body": truncateBytes(raw, 300), "try": loginTry})
		if cdType != "invalid_or_expired_password" {
			writeAccountJSON(accountDir, map[string]any{"email": email, "password": password, "status": "login_with_email_failed", "step": 6, "status_code": st})
			return registerGoResult{}, fmt.Errorf("loginWithEmail 没拿到 token_v2: status=%d body=%s", st, truncateBytes(raw, 300))
		}
	}
	if !loginOK {
		writeAccountJSON(accountDir, map[string]any{"email": email, "password": password, "status": "login_with_email_failed_retries_exhausted", "step": 6})
		return registerGoResult{}, fmt.Errorf("loginWithEmail 3 次均无效(疑似邮件延迟超过密码有效期)")
	}

	// Step 7: onboarding — getLifecycleUserProfile + getSpacesInitial
	lifecycleRaw, _, lerr := notionPost(ctx, hc, "/getLifecycleUserProfile", map[string]any{}, cv, registerNotionAppHome+"/", nil)
	lifecycle := decodeJSON(lifecycleRaw)
	trace.log(map[string]any{"step": 7, "action": "lifecycle_resp", "err": fmt.Sprint(lerr)})

	// gsi 偶发返回空/失败（网络抖动或登录态热同步延迟）——带重试并记录
	var gsiRaw []byte
	var gsiSt int
	var gsiErr error
	for ri := 0; ri < 3; ri++ {
		gsiRaw, gsiSt, gsiErr = notionPost(ctx, hc, "/getSpacesInitial", map[string]any{}, cv, registerNotionAppHome+"/", nil)
		if gsiErr == nil && gsiSt == 200 {
			break
		}
		trace.log(map[string]any{"step": 7, "action": "gsi_retry", "try": ri, "status": gsiSt, "err": fmt.Sprint(gsiErr)})
		if !sleepCtx(ctx, 10*time.Second) {
			goto gsiDone
		}
	}
gsiDone:
	gsi := decodeJSON(gsiRaw)
	userID, userName, spaceID := "", "", ""
	tier := ""
	if users, ok := gsi["users"].(map[string]any); ok {
		for uid, entry := range users {
			userID = uid
			if em, ok := entry.(map[string]any); ok {
				if nu, ok := em["notion_user"].(map[string]any); ok {
					if vv, ok := nu["value"].(map[string]any); ok {
						userName, _ = vv["name"].(string)
					}
				}
			}
			break
		}
	}
	spaceViewIDExisting := ""
	if spaces, ok := gsi["spaces"].(map[string]any); ok {
		// personal/invite 模式都先复用账号已有空间：
		// 老账号(8 个 hidesit 邮箱本身都有个人空间)实测 createspace 会 429(每日上限)
		for sid := range spaces {
			spaceID = sid
			break
		}
	}
	// getSpacesInitial 对老账号返回里往往没有 spaces 段（实测 concrete.sloth 2026-08-31），
	// 退而求其次：loadUserContent 的 recordMap.space / space_view 是全量权威来源
	if spaceID == "" {
		lucRaw, _, lucErr := notionPost(ctx, hc, "/loadUserContent", map[string]any{}, cv, registerNotionAppHome+"/", nil)
		luc := decodeJSON(lucRaw)
		if lucErr == nil {
			if rm, ok := luc["recordMap"].(map[string]any); ok {
				spaces, _ := rm["space"].(map[string]any)
				spaceViews, _ := rm["space_view"].(map[string]any)
				viewOf := map[string]string{}
				for vid, vv := range spaceViews {
					if vm, ok := vv.(map[string]any); ok {
						if rec, ok := vm["value"].(map[string]any); ok {
							if rec2, ok := rec["value"].(map[string]any); ok {
								if sid, _ := rec2["spaceId"].(string); sid != "" {
									viewOf[sid] = vid
								}
							}
						}
					}
				}
				// 只收本账号建的空间（created_by_id == userID）：
				// selfJoin 进来的共享空间（b17d 实测）quota 归属原订阅方，用完不回，
				// 全账号群聚死撞同一陷阱。
				var ownSpace string
				for sid, sv := range spaces {
					if sm, ok := sv.(map[string]any); ok {
						rec, _ := sm["value"].(map[string]any)
						rec2, _ := rec["value"].(map[string]any)
						if rec2 == nil {
							rec2 = rec
						}
						if cb, _ := rec2["createdById"].(string); cb == userID {
							ownSpace = sid
							break
						}
					}
				}
				if ownSpace == "" {
					trace.log(map[string]any{"step": 7, "action": "no_owned_space_skip_joined", "n_spaces": len(spaces)})
				}
				spaceID = ownSpace
				if spaceID != "" {
					spaceViewIDExisting = viewOf[spaceID]
					trace.log(map[string]any{"step": 7, "action": "load_user_content_space", "space_id": spaceID, "space_view_id": spaceViewIDExisting, "n_spaces": len(spaces)})
				}
			}
		} else {
			trace.log(map[string]any{"step": 7, "action": "load_user_content_err", "err": fmt.Sprint(lucErr)})
		}
	}

	// 已有空间顺带挖出它的 spaceViewId（getSpacesInitial 里 view 节点 value 带 spaceId，key 是 view uuid）
	if spaceID != "" {
		var scan func(v any, depth int) string
		scan = func(v any, depth int) string {
			if depth > 5 {
				return ""
			}
			m, ok := v.(map[string]any)
			if !ok {
				return ""
			}
			for k, val := range m {
				vm, ok := val.(map[string]any)
				if !ok {
					continue
				}
				if sid, _ := vm["spaceId"].(string); sid == spaceID && k != spaceID && len(k) >= 32 {
					return k
				}
				if r := scan(vm, depth+1); r != "" {
					return r
				}
			}
			return ""
		}
		spaceViewIDExisting = scan(gsi, 0)
	}
	trace.log(map[string]any{"step": 7, "action": "ids_extracted", "user_id": userID, "user_name": userName, "space_id": spaceID})

	// Step 7.5: 拿 space_id — personal 直接自建；invite 先 getJoinableSpaces/selfJoinSpaceByDomain，
	// 无可 join 语料时回退自建 personal（Python 端留 TODO，Go 端补齐：白号又没同域可 join 空间时
	// 必须有兜底，否则 probe 无 space_id 不可用）。
	deviceIDForSpace := sessionCookieValue(hc, "notion_browser_id")
	if deviceIDForSpace == "" {
		deviceIDForSpace = deviceID
	}
	spaceViewID := spaceViewIDExisting
	if spaceID == "" && spaceMode != "personal" {
		rawJoin, stJoin, _ := notionPost(ctx, hc, "/getJoinableSpaces",
			map[string]any{"excludeUnactionableSpaces": false}, cv, registerNotionAppHome+"/onboarding", nil)
		j := decodeJSON(rawJoin)
		results, _ := j["results"].([]any)
		trace.log(map[string]any{"step": "7.5", "action": "joinable_spaces_resp", "status": stJoin, "n_results": len(results)})
		var chosen map[string]any
		for _, it := range results {
			m, ok := it.(map[string]any)
			if !ok || m["joinabilityStatus"] != "CAN_JOIN" {
				continue
			}
			ti, _ := m["subscriptionTier"].(string)
			if ti != "" && ti != "free" {
				chosen = m
				break
			}
			if chosen == nil {
				chosen = m
			}
		}
		if chosen != nil {
			joinSID, _ := chosen["id"].(string)
			tier, _ = chosen["subscriptionTier"].(string)
			jraw, jst, jerr := notionPost(ctx, hc, "/selfJoinSpaceByDomain", map[string]any{
				"spaceId": joinSID, "joinableSpacesViewType": "onboarding_list", "analyticsFrom": "onboarding_member",
			}, cv, registerNotionAppHome+"/onboarding", nil)
			trace.log(map[string]any{"step": "7.5", "action": "self_join_resp", "status": jst, "err": fmt.Sprint(jerr)})
			if jerr == nil && jst == 200 && joinSID != "" {
				spaceID = joinSID
			}
			_ = jraw
		}
	}
	if spaceID == "" {
		// 自建 personal（9 字段精确 body；deviceId = notion_browser_id cookie）
		// 新号首个 createspace 服务端可 >30s，临时放宽本链 client 超时
		prevTimeout := hc.Timeout
		hc.Timeout = 120 * time.Second
		defer func() { hc.Timeout = prevTimeout }()
		trace.log(map[string]any{"step": "7.5", "action": "self_build_personal_start"})
		local := email
		if at := strings.Index(email, "@"); at >= 0 {
			local = email[:at]
		}
		if len(local) > 12 {
			local = local[:12]
		}
		var raw []byte
		var st int
		var err error
		for retryI := 0; retryI < 3; retryI++ {
			raw, st, err = notionPost(ctx, hc, "/createspace", map[string]any{
			"name":           local + "'s Space",
			"icon":           "🏠",
			"planType":       "personal",
			"planSelection":  "personal",
			"initialPersona": "unfilled",
			"deviceId":       deviceIDForSpace,
			"deviceType":     "web-desktop",
			"source":         "handle_root_redirect",
			"createSpaceView": true,
			}, cv, registerNotionAppHome+"/onboarding", nil)
			// status=0 系网络层失败（连接被掐/超时），短暂重试
			if err != nil || st == 0 {
				trace.log(map[string]any{"step": "7.5", "action": "createspace_retry", "try": retryI, "status": st, "err": fmt.Sprint(err)})
				if retryI < 2 {
					if !sleepCtx(ctx, 15*time.Second) {
						break
					}
					continue
				}
			}
			break
		}
		if err == nil {
			if cj := decodeJSON(raw); cj != nil {
				if sid, _ := cj["spaceId"].(string); sid != "" {
					spaceID = sid
					tier = "personal"
				}
				if svp, ok := cj["spaceViewPointer"].(map[string]any); ok {
					spaceViewID, _ = svp["id"].(string)
				}
			}
		}
		trace.log(map[string]any{"step": "7.5", "action": "self_build_personal_resp", "status": st, "space_id": spaceID, "space_view_id": spaceViewID})
		if spaceID == "" {
			// 极端：createspace 全败（Notion 边缘 504/网络抖动）。保留账号本体（token_v2/密码都在），
			// 空间让 space_pool 预建流程稍后补建，不浪费邮箱+账号。
			if registerAllowSpaceless {
				trace.log(map[string]any{"step": "7.5", "action": "spaceless_continue"})
			} else {
				writeAccountJSON(accountDir, map[string]any{"email": email, "password": password, "status": "space_acquire_failed", "step": "7.5", "status_code": st})
				return registerGoResult{}, fmt.Errorf("createspace 未获 space_id: status=%d body=%s", st, truncateBytes(raw, 300))
			}
		}
	}

	// Step 8: getAvailableModels → models blob，落盘 probe.json / account.json / trace / 桶
	trace.log(map[string]any{"step": 8, "action": "compose_session"})
	modelsBlob := ""
	if spaceID != "" {
		mraw, mst, merr := notionPost(ctx, hc, "/getAvailableModels", map[string]any{"spaceId": spaceID}, cv,
			registerNotionAppHome+"/", map[string]string{"x-notion-space-id": spaceID})
		if merr == nil {
			if mj := decodeJSON(mraw); mj != nil {
				if arr, ok := mj["models"].([]any); ok && len(arr) > 0 {
					enc, _ := json.Marshal(map[string]any{"models": arr})
					modelsBlob = string(enc)
					trace.log(map[string]any{"step": 8, "action": "models_blob_ok", "n": len(arr)})
				}
			}
		}
		if modelsBlob == "" {
			trace.log(map[string]any{"step": 8, "action": "models_blob_skip", "status": mst})
		}
	}

	cookiesFull := sessionCookiesFull(hc)
	probeCookies := make([]map[string]string, 0, len(cookiesFull))
	for _, c := range cookiesFull {
		probeCookies = append(probeCookies, map[string]string{"name": c["name"], "value": c["value"]})
	}
	probe := map[string]any{
		"email":          email,
		"user_id":        userID,
		"user_name":      userName,
		"space_id":       spaceID,
		"client_version": cv,
		"cookies":        probeCookies,
	}
	if spaceViewID != "" {
		probe["space_view_id"] = spaceViewID
	}
	if modelsBlob != "" {
		probe["models"] = modelsBlob
	}
	probePath := filepath.Join(accountDir, "probe.json")
	if err := writeJSONFile(probePath, probe); err != nil {
		return registerGoResult{}, fmt.Errorf("write probe.json: %w", err)
	}

	spaceSource := "none"
	if spaceID != "" {
		if tier == "personal" {
			spaceSource = "createspace_personal"
		} else {
			spaceSource = "selfJoinSpaceByDomain"
		}
	}
	account := map[string]any{
		"email":               email,
		"password":            password,
		"device_id":           deviceID,
		"browser_id":          browserID,
		"client_version":      cv,
		"login_options_token": loToken,
		"csrf_state":          csrfState,
		"user_id":             userID,
		"user_name":           userName,
		"space_id":            spaceID,
		"space_view_id":       spaceViewID,
		"space_id_source":     spaceSource,
		"tier":                tier,
		"token_v2":            tokenV2,
		"notion_cookies":      cookiesFull,
		"lifecycle":           lifecycle,
		"created_at":          time.Now().UTC().Format(time.RFC3339),
	}
	if country != "" {
		account["country"] = country
	}
	// 隐匿验证码（Python 存明文，Go 侧不再留——只用于本次 loginWithEmail）
	writeAccountJSON(accountDir, account)

	// 归档 trace 副本到账号目录
	traceName := "trace_" + strings.NewReplacer(":", "", "+", "", "-", "").Replace(time.Now().UTC().Format("2006-01-02T150405")) + ".jsonl"
	if traceSrc, rerr := os.ReadFile(filepath.Join(logsDir, "last_trace.jsonl")); rerr == nil {
		_ = os.WriteFile(filepath.Join(accountDir, traceName), traceSrc, 0o644)
	}

	// 桶规则：50 号一桶 accounts/barrel_<NNNN>/accounts.txt（tab 分隔，对齐 Python 输出）
	barrel, seq, berr := appendAccountToBarrel(root, map[string]string{
		"email": email, "password": password, "user_id": userID, "space_id": spaceID,
		"space_source": spaceSource, "tier": tier, "token_v2": tokenV2, "client_version": cv,
		"created_at": account["created_at"].(string),
	})
	if berr != nil {
		log.Printf("[register_chain] barrel append failed: %v", berr)
	}

	// 清理重试途中残留的旧 email 兜底目录（只留最终成功那个）
	finalDir := filepath.Clean(accountDir)
	for _, d := range triedDirs {
		if filepath.Clean(d) == finalDir {
			continue
		}
		_ = os.RemoveAll(d)
	}

	trace.log(map[string]any{"phase": "end", "status": "success", "user_id": userID, "space_id": spaceID, "out": accountDir, "barrel": barrel, "barrel_seq": seq})
	log.Printf("[register_chain] registered %s user_id=%s space_id=%s source=%s", email, userID, spaceID, spaceSource)
	return registerGoResult{
		Email: email, UserID: userID, SpaceID: spaceID,
		AccountDir: accountDir, ProbePath: probePath,
		Barrel: barrel, BarrelSeq: seq, ClientVer: cv,
	}, nil
}

// ── 桶 / 工具函数 ───────────────────────────────────────────────────────────

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

var barrelMu sync.Mutex

// appendAccountToBarrel — 追加/开桶；返回 barrel 名 + 桶内序号
func appendAccountToBarrel(root string, rec map[string]string) (string, string, error) {
	barrelMu.Lock()
	defer barrelMu.Unlock()
	accountsRoot := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accountsRoot, 0o755); err != nil {
		return "", "", err
	}
	entries, _ := os.ReadDir(accountsRoot)
	var barrels []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "barrel_") {
			barrels = append(barrels, e.Name())
		}
	}
	sortStrings(barrels)
	targetDir := ""
	for _, b := range barrels {
		f := filepath.Join(accountsRoot, b, "accounts.txt")
		n := countLines(f)
		if n < registerBarrelSize {
			targetDir = b
			break
		}
	}
	if targetDir == "" {
		targetDir = barrelName(len(barrels) + 1)
		_ = os.MkdirAll(filepath.Join(accountsRoot, targetDir), 0o755)
	}
	fpath := filepath.Join(accountsRoot, targetDir, "accounts.txt")
	idx := countLines(fpath) + 1
	line := strings.Join([]string{
		shortSeq(idx), rec["email"], rec["password"], rec["user_id"], rec["space_id"],
		rec["space_source"], rec["tier"], rec["token_v2"], rec["client_version"], rec["created_at"],
	}, "\t") + "\n"
	f, err := os.OpenFile(fpath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return "", "", err
	}
	return targetDir, shortSeq(idx), nil
}

func barrelName(n int) string { return "barrel_" + pad4(n) }
func pad4(n int) string {
	s := fmt.Sprintf("%d", n)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
func shortSeq(idx int) string { return "n" + pad4(idx) }

func countLines(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := strings.Count(string(raw), "\n")
	return n
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func writeAccountJSON(dir string, v map[string]any) {
	_ = writeJSONFile(filepath.Join(dir, "account.json"), v)
}

func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// truncateStr — 截断长文本用于错误消息
func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
