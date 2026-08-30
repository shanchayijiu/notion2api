package app

// register_provider.go — P2 号源闭环：调用注册机子进程（Python 脚本）补号入池。
// 2026 Phase2 整改：脚本目录/解释器/代理全部走 cfg.Register，不再硬编码 Windows 路径；
// 脚本未配置时优雅报"unavailable"；并附池水位自动巡检（低于 min_healthy 自动补号）。

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 号源未配置（脚本目录为空或脚本文件不存在）：手动补号返回 503，巡检记日志后跳过。
var errRegisterNotConfigured = fmt.Errorf("register script not configured (set register.script_dir)")

type registerResolver struct {
	dir     string
	script  string
	python  string
	proxy   string
	timeout time.Duration
}

func resolveRegisterFromConfig(cfg AppConfig) registerResolver {
	rc := cfg.ResolveRegister()
	return registerResolver{
		dir:     resolveConfigRelativePath(cfg.ConfigPath, rc.ScriptDir, ""),
		script:  rc.ScriptName,
		python:  rc.PythonBin,
		proxy:   strings.TrimSpace(rc.Proxy),
		timeout: time.Duration(rc.TimeoutSec) * time.Second,
	}
}

// RegisterNewAccount — 调注册机产一个新号并入池。
// 返回新号 email；号目录在 register.script_dir/accounts/detail/<email>/。
func (a *App) RegisterNewAccount(ctx context.Context, proxy string) (string, error) {
	if a == nil || a.State == nil {
		return "", fmt.Errorf("server state unavailable")
	}
	cfg, _, _ := a.State.Snapshot()
	reg := resolveRegisterFromConfig(cfg)
	if strings.TrimSpace(reg.dir) == "" || !fileExists(filepath.Join(reg.dir, reg.script)) {
		return "", errRegisterNotConfigured
	}
	if proxy == "" {
		proxy = reg.proxy
	}

	args := []string{reg.script, "--n", "1", "--gap", "10"}
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	runCtx, cancel := context.WithTimeout(ctx, reg.timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, reg.python, args...)
	cmd.Dir = reg.dir
	// 强制 UTF-8，避免输出乱码导致邮箱解析失败
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	startTime := time.Now()
	runErr := cmd.Run()
	output := outBuf.String()
	if runErr != nil {
		// 注册机常在产号成功后以非 0 退出（后续步骤报错），只要能定位到本次新建的账号即视为成功
		log.Printf("[register_provider] subprocess exit err=%v; will try locate created account", runErr)
	}
	email, parseErr := extractRegisteredEmail(output)
	if parseErr != nil {
		// 兜底：注册机输出不可解析时，按 probe.json 落盘时间定位本次新建的账号
		if recent, rErr := findAccountCreatedSince(reg.dir, startTime); rErr == nil {
			email = recent
			log.Printf("[register_provider] parsed email missing; located newly created account %s", email)
		} else if runErr != nil {
			return "", fmt.Errorf("register subprocess failed: %w stderr=%s", runErr, truncateStr(errBuf.String(), 300))
		} else {
			log.Printf("[register_provider] subprocess output: %s", truncateStr(output, 800))
			return "", parseErr
		}
	}

	// 入池：追加 cfg.Accounts 并通过 SaveAndApply 刷新 dispatch 快照，
	// 否则新号只写存储、不进实时候选队列 — 等于没入池。
	probePath := filepath.Join(reg.dir, "accounts", "detail", email, "probe.json")
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
	err := a.State.saveAndApplyLocked(cfg2)
	a.State.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("save accounts failed: %w", err)
	}
	log.Printf("[register_provider] new account %s registered and pooled (probe=%s)", email, probePath)
	return email, nil
}

// findAccountCreatedSince — 注册机输出不可解析时，按 accounts/detail/<email>/probe.json
// 落盘时间定位本次新建的账号（注册约 30-40s，取 startTime 之后最新创建者）。
func findAccountCreatedSince(scriptDir string, since time.Time) (string, error) {
	root := filepath.Join(scriptDir, "accounts", "detail")
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		probe := filepath.Join(root, e.Name(), "probe.json")
		info, err := os.Stat(probe)
		if err != nil {
			continue
		}
		mt := info.ModTime()
		if mt.After(since) && mt.After(bestMod) {
			bestMod = mt
			best = e.Name()
		}
	}
	if best == "" {
		return "", fmt.Errorf("no account created since %s", since.Format(time.RFC3339))
	}
	return best, nil
}

// extractRegisteredEmail — 从 batch_run_proto 输出提取成功邮箱
func extractRegisteredEmail(output string) (string, error) {
	// 输出形如: [=== 1/1 ===] OK 成功: mt6puecq4ubt@imageeditgpt.com  space_id=...
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "OK 成功:") {
			rest := line[strings.Index(line, "OK 成功:")+len("OK 成功:"):]
			rest = strings.TrimSpace(rest)
			if at := strings.Index(rest, " "); at > 0 {
				rest = rest[:at]
			}
			if strings.Contains(rest, "@") {
				return rest, nil
			}
		}
	}
	return "", fmt.Errorf("no registered email in output")
}

// truncateStr — 截断长文本用于错误消息
func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// ── 手动补号（HTTP 入口）──────────────────────────────────────────────

// adminRegisterAccount — POST /admin/accounts/register 手动补号
func (a *App) adminRegisterAccount(w http.ResponseWriter, r *http.Request) {
	proxy := strings.TrimSpace(r.URL.Query().Get("proxy"))
	email, err := a.RegisterNewAccount(r.Context(), proxy)
	if err != nil {
		if err == errRegisterNotConfigured {
			writeOpenAIError(w, http.StatusServiceUnavailable, err.Error(), "not_configured", "register_not_configured")
			return
		}
		writeOpenAIError(w, http.StatusBadGateway, "register failed: "+err.Error(), "server_error", "register_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "email": email})
}

// ── 池水位自动巡检（P2 自动补给）──────────────────────────────────────

// startAccountReconcilerLoop — 后台巡检：池内健康账号数 < min_healthy 时自动调注册机补 1 个。
// register.enabled=false 时循环空转（每次 tick 看一眼配置,热更配置即生效）。
func (a *App) startAccountReconcilerLoop(ctx context.Context) {
	for {
		cfg, _, _ := a.State.Snapshot()
		rc := cfg.ResolveRegister()
		interval := time.Duration(rc.CheckIntervalSec) * time.Second
		if rc.Enabled {
			a.reconcileAccountPoolLevel(ctx, rc)
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
	if healthy >= rc.MinHealthy {
		return
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(rc.TimeoutSec)*time.Second)
	defer cancel()
	email, err := a.RegisterNewAccount(ctx, rc.Proxy)
	if err != nil {
		log.Printf("[register_reconciler] auto-replenish failed: %v", err)
		return
	}
	log.Printf("[register_reconciler] auto-replenished account=%s", email)
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
