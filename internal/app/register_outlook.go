package app

// register_outlook.go — Outlook 邮箱池接入（NAS 上 outlook-mail 容器，纯 HTTP）。
// 链上语义对齐 adguard/mailtm provider：挑一个还没注册成功过的 outlook 地址注册，
// 轮询 /api/emails/<addr>（每次实时打 Graph/IMAP）等 Notion 验证码邮件。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type outlookPool struct {
	base     string
	password string
	hc       *http.Client
	proxy    string
}

type outlookAccount struct {
	ID     int    `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
	Type   string `json:"account_type"`
}

func newOutlookPool(base, password, proxy string) *outlookPool {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 45 * time.Second}
	return &outlookPool{base: strings.TrimRight(strings.TrimSpace(base), "/"), password: password, hc: hc, proxy: proxy}
}

// ensureLogin — 未登录或会话过期则重登；登录即拿 /api/version-status 200 判定。
func (o *outlookPool) ensureLogin(ctx context.Context) error {
	ok, _ := o.tryGet(ctx, "/api/version-status")
	if ok {
		return nil
	}
	form := url.Values{"password": {o.password}}
	req, _ := http.NewRequestWithContext(ctx, "POST", o.base+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", registerUA)
	resp, err := o.hc.Do(req)
	if err != nil {
		return fmt.Errorf("outlookmail login: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"success":true`) {
		return fmt.Errorf("outlookmail login failed: status=%d body=%s", resp.StatusCode, truncateBytes(body, 120))
	}
	return nil
}

// tryGet — 已登录返回 true（status 200），401/403 重登。
func (o *outlookPool) tryGet(ctx context.Context, path string) (bool, []byte) {
	req, _ := http.NewRequestWithContext(ctx, "GET", o.base+path, nil)
	req.Header.Set("User-Agent", registerUA)
	resp, err := o.hc.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 {
		return true, body
	}
	return false, body
}

func (o *outlookPool) getJSON(ctx context.Context, path string) ([]byte, error) {
	if err := o.ensureLogin(ctx); err != nil {
		return nil, err
	}
	ok, body := o.tryGet(ctx, path)
	if !ok {
		// 再强制登录重试一次
		req, _ := http.NewRequestWithContext(ctx, "POST", o.base+"/login", strings.NewReader(url.Values{"password": {o.password}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", registerUA)
		resp, err := o.hc.Do(req)
		if err == nil {
			io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		ok, body = o.tryGet(ctx, path)
		if !ok {
			return nil, fmt.Errorf("outlookmail GET %s failed", path)
		}
	}
	return body, nil
}

// listAccounts — 拉全量 outlook 账号（active only），分页 page_size=500。
func (o *outlookPool) listAccounts(ctx context.Context) ([]outlookAccount, error) {
	var out []outlookAccount
	for page := 1; page <= 10; page++ {
		body, err := o.getJSON(ctx, fmt.Sprintf("/api/accounts?page=%d&page_size=500", page))
		if err != nil {
			return nil, err
		}
		var arr struct {
			Accounts []outlookAccount `json:"accounts"`
			Total    int              `json:"total"`
		}
		if err := json.Unmarshal(body, &arr); err != nil {
			return nil, fmt.Errorf("outlookmail /api/accounts parse: %w", err)
		}
		for _, a := range arr.Accounts {
			if strings.EqualFold(a.Status, "active") && strings.EqualFold(a.Type, "outlook") {
				out = append(out, a)
			}
		}
		if len(arr.Accounts) < 500 {
			break
		}
	}
	return out, nil
}

// outlookPickAddress — 挑没被注册用过的邮箱：detail/<addr>/probe.json 不存在即可。
func outlookPickAddress(accounts []outlookAccount, detailRoot string) (outlookAccount, error) {
	if len(accounts) == 0 {
		return outlookAccount{}, fmt.Errorf("outlook pool empty")
	}
	start := rand.Intn(len(accounts))
	for i := 0; i < len(accounts); i++ {
		a := accounts[(start+i)%len(accounts)]
		addr := strings.ToLower(strings.TrimSpace(a.Email))
		if addr == "" {
			continue
		}
		dir := filepath.Join(detailRoot, strings.ReplaceAll(addr, "/", "_"))
		// 目录已存在即视为该邮箱已烧：成功（probe.json 在）或失败（只有 account.json）都不再复用
		if _, err := os.Stat(dir); err == nil {
			continue
		}
		return a, nil
	}
	return outlookAccount{}, fmt.Errorf("all %d outlook accounts already registered", len(accounts))
}

// outlookWaitCode — 轮询 /api/emails/<addr> 实时拉信；notBefore 用作粗糙时间预约。
func outlookWaitCode(ctx context.Context, pool *outlookPool, addr string, notBefore time.Time) (string, error) {
	deadline := time.Now().Add(840 * time.Second)
	polls := 0
	addrEsc := url.PathEscape(strings.ToLower(addr))
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		polls++
		body, err := pool.getJSON(ctx, fmt.Sprintf("/api/emails/%s?page=1&page_size=5", addrEsc))
		if err != nil {
			log.Printf("[register_mail] outlook poll #%d mailbox=%s error: %v", polls, addr, err)
		} else {
			var list struct {
				Emails []struct {
					ID      string `json:"id"`
					From    string `json:"from"`
					Subject string `json:"subject"`
					Date    string `json:"date"`
					Preview string `json:"body_preview"`
				} `json:"emails"`
			}
			_ = json.Unmarshal(body, &list)
			log.Printf("[register_mail] outlook poll #%d mailbox=%s seen_msgs=%d", polls, addr, len(list.Emails))
			for _, m := range list.Emails {
				if !strings.Contains(strings.ToLower(m.From), "notion") && !strings.Contains(strings.ToLower(m.Subject), "notion") && !strings.Contains(strings.ToLower(m.Subject), "temporary") {
					continue
				}
				// 优先抽主题/预览里的 6 位
				if c := extractNotionCode(m.Subject + "\n" + m.Preview); c != "" {
					return c, nil
				}
				// 取全文（魔链变体密码在 href 里）
				if full, ferr := pool.getJSON(ctx, fmt.Sprintf("/api/email/%s/%s", addrEsc, url.PathEscape(m.ID))); ferr == nil {
					var msg struct {
						Email struct {
							Body string `json:"body"`
						} `json:"email"`
					}
					_ = json.Unmarshal(full, &msg)
					hay := m.Subject + "\n" + stripHTML(msg.Email.Body) + "\n" + msg.Email.Body
					if c := extractNotionCode(hay); c != "" {
						return c, nil
					}
				}
			}
		}
		if !sleepCtx(ctx, 20*time.Second) {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("outlook poll timeout after 840s")
}
