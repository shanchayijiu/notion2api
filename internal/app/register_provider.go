package app

// register_provider.go — P2 号源闭环：纯 Go 注册链（register_chain.go）补号入池。
// 2026-08-30 重排：移除 Python 子进程（batch_run_proto.py），注册链直接内联跑
// （register_mail.go + register_chain.go，纯 HTTP，Docker 容器内原生运行，无 python/playwright）。
// 保留：池水位自动巡检（低于 min_healthy 自动补号）与手动补号 HTTP 入口。

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// registerRoots — 注册根（OutputRoot 下的 accounts/ logs/ mailboxes/）：优先 script_dir
// （脚本目录即原 Python 注册根），未配置时退回登录助手会话目录的兄弟 register/。
func resolveRegisterRoot(cfg AppConfig) string {
	rc := cfg.ResolveRegister()
	if dir := strings.TrimSpace(rc.ScriptDir); dir != "" {
		return resolveConfigRelativePath(cfg.ConfigPath, dir, "")
	}
	helper := cfg.ResolveLoginHelper()
	base := helper.SessionsDir
	if strings.TrimSpace(base) == "" {
		base = "probe_files/notion_accounts"
	}
	return filepath.Join(filepath.Dir(base), "register")
}

// RegisterNewAccount — 纯 Go 注册一个新号并入池。
// 返回新号 email；号目录在 <registerRoot>/accounts/detail/<email>/（probe.json/account.json）。
func (a *App) RegisterNewAccount(parent context.Context, proxy string) (string, error) {
	if a == nil || a.State == nil {
		return "", fmt.Errorf("server state unavailable")
	}
	cfg, _, _ := a.State.Snapshot()
	rc := cfg.ResolveRegister()
	if strings.TrimSpace(proxy) == "" {
		proxy = strings.TrimSpace(rc.Proxy)
	}
	root := resolveRegisterRoot(cfg)

	ctx, cancel := context.WithTimeout(parent, time.Duration(rc.TimeoutSec)*time.Second)
	defer cancel()

	res, err := registerOneGo(ctx, registerGoOptions{
		Proxy:           proxy,
		MailProvider:    rc.MailProvider,
		SpaceMode:       rc.SpaceMode,
		OutputRoot:      root,
		OutlookBaseURL:  strings.TrimSpace(rc.OutlookBaseURL),
		OutlookPassword: strings.TrimSpace(rc.OutlookPassword),
	})
	if err != nil {
		return "", err
	}
	email := res.Email
	probePath := res.ProbePath
	if !fileExists(probePath) {
		return "", fmt.Errorf("probe not found for %s", email)
	}
	key := canonicalEmailKey(email)
	newAccount := NotionAccount{
		Email:     email,
		ProbeJSON: probePath,
		Priority:  100,
		Status:    "ready",
	}

	a.State.mu.Lock()
	deduped := false
	for _, acc := range a.State.Config.Accounts {
		if canonicalEmailKey(acc.Email) == key {
			deduped = true
			break
		}
	}
	if deduped {
		a.State.mu.Unlock()
		log.Printf("[register_provider] account %s already pooled", email)
		return email, nil
	}
	cfg2 := a.State.Config
	cfg2.Accounts = append(cfg2.Accounts, newAccount)
	err = a.State.saveAndApplyLocked(cfg2)
	a.State.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("save accounts failed: %w", err)
	}
	log.Printf("[register_provider] new account %s registered and pooled (probe=%s)", email, probePath)
	return email, nil
}

// ── 手动补号（HTTP 入口）──────────────────────────────────────────────

// adminRegisterAccount — POST /admin/accounts/register 手动补号
func (a *App) adminRegisterAccount(w http.ResponseWriter, r *http.Request) {
	proxy := strings.TrimSpace(r.URL.Query().Get("proxy"))
	email, err := a.RegisterNewAccount(r.Context(), proxy)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "register failed: "+err.Error(), "server_error", "register_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": email})
}

// ── 池水位自动巡检（P2 自动补给）──────────────────────────────────────

// startAccountReconcilerLoop — 后台巡检：池内健康账号数 < min_healthy 时自动补 1 个。
// register.enabled=false 时循环空转（每次 tick 看一眼配置,热更配置即生效）。
func (a *App) startAccountReconcilerLoop(ctx context.Context) {
	for {
		cfg, _, _ := a.State.Snapshot()
		rc := cfg.ResolveRegister()
		interval := time.Duration(rc.CheckIntervalSec) * time.Second
		if rc.Enabled {
			a.reconcileAccountPoolLevel(ctx, rc)
		}
		// 欠水位时立刻进入下一巡，直到补满；满水位按周期巡检
		healthy, _ := countHealthyAccounts(cfg)
		if healthy < rc.MinHealthy && rc.Enabled {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// reconcileAccountPoolLevel — 单次巡检：计算健康水位，低于 min_healthy 补 1 个
// （慢节奏 + 单次 1 个：防注册机/上游被注册流量打爆）
func (a *App) reconcileAccountPoolLevel(parent context.Context, rc RegisterConfig) {
	cfg, _, _ := a.State.Snapshot()
	healthy, total := countHealthyAccounts(cfg)
	log.Printf("[register_reconciler] pool health: healthy=%d total=%d min_healthy=%d", healthy, total, rc.MinHealthy)
	deficit := rc.MinHealthy - healthy
	if deficit <= 0 {
		return
	}
	maxP := rc.MaxParallel
	if maxP <= 0 {
		maxP = 3
	}
	if deficit > maxP {
		deficit = maxP
	}
	var wg sync.WaitGroup
	for i := 0; i < deficit; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			// 错峰启动：注册/邮件服务都不宜瞬时并发
			if slot > 0 {
				select {
				case <-parent.Done():
					return
				case <-time.After(time.Duration(slot) * 45 * time.Second):
				}
			}
			ctx, cancel := context.WithTimeout(parent, time.Duration(rc.TimeoutSec)*time.Second)
			defer cancel()
			email, err := a.RegisterNewAccount(ctx, rc.Proxy)
			if err != nil {
				log.Printf("[register_reconciler] auto-replenish failed (slot=%d): %v", slot, err)
				return
			}
			log.Printf("[register_reconciler] auto-replenished account=%s (slot=%d)", email, slot)
		}(i)
	}
	wg.Wait()
}

// countHealthyAccounts — 统计池内"当前可用"账号数（非 disabled、有 artifact、不在冷却）
func countHealthyAccounts(cfg AppConfig) (healthy, total int) {
	now := time.Now()
	for _, acc := range cfg.Accounts {
		total++
		if acc.Disabled {
			continue
		}
		expiry := accountCooldownExpiry(acc)
		if !expiry.IsZero() && now.Before(expiry) {
			continue
		}
		acc = ensureAccountPaths(cfg, acc)
		if !accountHasUsableArtifacts(cfg, acc) {
			continue
		}
		healthy++
	}
	return healthy, total
}
