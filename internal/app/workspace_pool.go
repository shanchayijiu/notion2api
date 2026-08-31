package app

// workspace_pool.go — 工作空间配额恢复轮换池（space_pool）：
//   用户洞察（2026-08-30）：工作空间额度会随时间自动恢复，号够多时无需反复增删工作空间。
//   策略 = 一次创建到 target_per_account → 额度归零标记 cooldown → 冷却期内轮换到
//   池里另一 active 空间 → 定时器冷却到点自动恢复该空间为 active。
//   全程不删空间；createspace 只发生在建池阶段（每日 ~6 个/号上限，预建是摊销成本）。

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"strings"
	"time"
)

// startSpacePoolLoop — 后台巡检：冷却到点自动恢复 + 池水位补齐。
// space_pool.enabled=false 时空转（tick 时回看配置，热更生效）。
func (a *App) startSpacePoolLoop(ctx context.Context) {
	for {
		cfg, _, _ := a.State.Snapshot()
		sc := cfg.ResolveSpacePool()
		if sc.Enabled && a.State.Store != nil {
			a.recoverExpiredSpaceCooldowns(cfg)
			a.ensureSpacePoolToppedUp(ctx, cfg, sc)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(sc.CheckIntervalSec) * time.Second):
		}
	}
}

// recoverExpiredSpaceCooldowns — 冷却到点的空间自动恢复为 active（不重建不删除）
func (a *App) recoverExpiredSpaceCooldowns(cfg AppConfig) {
	items, err := a.State.Store.LoadSpaceLifecycles("", spaceStatusCooldown)
	if err != nil || len(items) == 0 {
		return
	}
	now := time.Now()
	recovered := 0
	for _, lc := range items {
		until := parseLifecycleTime(lc.CooldownUntil)
		if until.IsZero() || now.Before(until) {
			continue
		}
		if err := a.State.Store.ClearSpaceLifecycleCooldown(lc.SpaceID); err != nil {
			log.Printf("[workspace_pool] recover space %s failed: %v", lc.SpaceID, err)
			continue
		}
		recovered++
	}
	if recovered > 0 {
		log.Printf("[workspace_pool] recovered %d cooldown space(s) to active", recovered)
	}
}

// countPoolSpaces — 池内空间数（active + cooldown 都算占位；exhausted/to_delete 为历史遗留不计）
func countPoolSpaces(items []SpaceLifecycle) (active, cooldown int) {
	for _, lc := range items {
		switch lc.Status {
		case spaceStatusActive:
			active++
		case spaceStatusCooldown:
			cooldown++
		}
	}
	return active, cooldown
}

// precreateFailCool — createspace 已基于 429 "recently submitted" 示得极限：
// 账号窗口在每次请求时刷新，继续轰炸永不开端口。per-account 指数退避：
// 第 n 次连续失败 -> 30m * 2^(n-1)（封顶 8h），预建成功后计数重置。
var precreateFailCool = struct {
	mu    sync.Mutex
	until map[string]time.Time
	fails map[string]int
}{until: map[string]time.Time{}, fails: map[string]int{}}

const precreateFailBaseCooldown = 30 * time.Minute
const precreateFailMaxCooldown = 8 * time.Hour

func precreateRecentlyFailed(email string) bool {
	precreateFailCool.mu.Lock()
	defer precreateFailCool.mu.Unlock()
	until, ok := precreateFailCool.until[email]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(precreateFailCool.until, email)
		return false
	}
	return true
}

func markPrecreateFailed(email string) {
	precreateFailCool.mu.Lock()
	defer precreateFailCool.mu.Unlock()
	n := precreateFailCool.fails[email] + 1
	precreateFailCool.fails[email] = n
	cool := precreateFailBaseCooldown
	for i := 1; i < n; i++ {
		cool *= 2
		if cool > precreateFailMaxCooldown {
			cool = precreateFailMaxCooldown
			break
		}
	}
	precreateFailCool.until[email] = time.Now().Add(cool)
}

func markPrecreateSucceeded(email string) {
	precreateFailCool.mu.Lock()
	defer precreateFailCool.mu.Unlock()
	precreateFailCool.fails[email] = 0
	delete(precreateFailCool.until, email)
}

// ensureSpacePoolToppedUp — 每号预建到 target 个空间（active+cooldown 合计数）。
// 每个 tick 每号最多创建 1 个，且尊重单账号 rotateMinInterval（贴近真实用户节奏，
// 防每日 createspace 配额打爆/账号级 429 风控）。
func (a *App) ensureSpacePoolToppedUp(ctx context.Context, cfg AppConfig, sc SpacePoolConfig) {
	target := sc.TargetPerAccount
	for _, acc := range cfg.Accounts {
		if !cfgSpaceEligible(cfg, acc) {
			continue
		}
		items, err := a.State.Store.LoadSpaceLifecycles(acc.Email, "")
		if err != nil {
			log.Printf("[workspace_pool] load spaces for %s failed: %v", acc.Email, err)
			continue
		}
		active, cooldown := countPoolSpaces(items)
		// 池内空间计入：active + cooldown；当前 probe 正在用的兜底空间若在表外不算（下一轮会被轮换记录）
		if active+cooldown >= target {
			continue
		}
		// 节流：最近 10min 内建过空间（无论状态）就跳过
		if lastCreated, ok := lastSpaceCreatedAt(items); ok && time.Since(lastCreated) < rotateMinInterval {
			continue
		}
		// 上 skę 预建失败 >= precreateFailCooldown 内热重试 = 账号 429 窗口会一直被重置
		if precreateRecentlyFailed(acc.Email) {
			continue
		}
		log.Printf("[workspace_pool] account %s pool low: active=%d cooldown=%d target=%d -> precreate 1", acc.Email, active, cooldown, target)
		if err := a.createPoolSpace(ctx, cfg, acc); err != nil {
			log.Printf("[workspace_pool] precreate failed for %s: %v", acc.Email, err)
			markPrecreateFailed(acc.Email)
		}
	}
}

// cfgSpaceEligible — 账号可用于预建空间：非 disabled、不在账号冷却、有可用 probe
func cfgSpaceEligible(cfg AppConfig, acc NotionAccount) bool {
	if acc.Disabled {
		return false
	}
	expiry := accountCooldownExpiry(acc)
	if !expiry.IsZero() && time.Now().Before(expiry) {
		return false
	}
	acc = ensureAccountPaths(cfg, acc)
	if !accountHasUsableArtifacts(cfg, acc) {
		return false
	}
	return strings.TrimSpace(acc.ProbeJSON) != ""
}

// lastSpaceCreatedAt — 池内最近一次 createspace 时间（节流依据）
func lastSpaceCreatedAt(items []SpaceLifecycle) (time.Time, bool) {
	var best time.Time
	for _, lc := range items {
		t := parseLifecycleTime(lc.CreatedAt)
		if t.After(best) {
			best = t
		}
	}
	return best, !best.IsZero()
}

// createPoolSpace — 为账号创建一个 personal 空间并记入池（不动当前 probe 指向；
// 预建是"池补充"，不切换活跃空间）。
func (a *App) createPoolSpace(ctx context.Context, cfg AppConfig, acc NotionAccount) error {
	acc = ensureAccountPaths(cfg, acc)
	session, err := loadSessionInfo(acc.ProbeJSON, acc.UserName, acc.SpaceName)
	if err != nil {
		return err
	}
	client := newNotionAIClient(session, cfg, acc.Email)
	rotator := a.rotator
	if rotator == nil {
		rotator = NewWorkspaceRotatorWithState(a.State.Store, a.State)
		a.rotator = rotator
	}
	deviceID := resolveAccountDeviceID(cfg, session)
	spaceID, viewID, err := rotator.createSpaceHTTP(ctx, client, deviceID)
	if err != nil {
		var apiErr *notionAPIError
		// 预建是低优先度兜底路径：任何 429（频率/日限）都不打账号冷却——
		// 池巡检下个 tick 自会再试；打账号冷却会把新注册号锁死一整天（2026-08-31 实测三回）
		_ = errors.As
		_ = apiErr
		_ = http.StatusTooManyRequests
		return err
	}
	if strings.TrimSpace(viewID) == "" {
		if vid, berr := rotator.bindSpaceHTTP(ctx, client, spaceID); berr == nil {
			viewID = vid
		} else {
			log.Printf("[workspace_pool] bind space %s view failed (will reuse via probe): %v", spaceID, berr)
		}
	}
	rotator.recordLifecycle(spaceID, acc.Email, spaceStatusActive, "", viewID)
	markPrecreateSucceeded(acc.Email)
	log.Printf("[workspace_pool] pre-created space %s for %s (view=%s)", spaceID, acc.Email, viewID)
	return nil
}
