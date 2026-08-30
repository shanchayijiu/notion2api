package app

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

const (
	accountCooldownBase        = 2 * time.Minute
	accountCooldownMax         = 30 * time.Minute
	accountAutoReloginInterval = 5 * time.Minute
)

func parseOptionalRFC3339(value string) time.Time {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, clean)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func formatRFC3339OrEmpty(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func resetAccountUsageWindow(account *NotionAccount, now time.Time) {
	if account == nil || account.HourlyQuota <= 0 {
		account.WindowStartedAt = ""
		account.WindowRequestCount = 0
		return
	}
	startedAt := parseOptionalRFC3339(account.WindowStartedAt)
	if startedAt.IsZero() || now.Sub(startedAt) >= time.Hour {
		account.WindowStartedAt = formatRFC3339OrEmpty(now)
		account.WindowRequestCount = 0
	}
}

func accountRemainingQuota(account NotionAccount, now time.Time) (int, bool) {
	if account.HourlyQuota <= 0 {
		return 0, false
	}
	resetAccountUsageWindow(&account, now)
	remaining := account.HourlyQuota - account.WindowRequestCount
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

func accountCooldownActive(account NotionAccount, now time.Time) bool {
	until := parseOptionalRFC3339(account.CooldownUntil)
	return !until.IsZero() && now.Before(until)
}

// accountCooldownExpiry — 冷却到期时间（无冷却记零值，排序时排最前）。
func accountCooldownExpiry(account NotionAccount) time.Time {
	return parseOptionalRFC3339(account.CooldownUntil)
}

func accountHasUsableArtifacts(cfg AppConfig, account NotionAccount) bool {
	account = ensureAccountPaths(cfg, account)
	return fileExists(account.ProbeJSON) || fileExists(account.StorageStatePath)
}

func accountDispatchEligible(cfg AppConfig, account NotionAccount, now time.Time) (bool, string) {
	account = ensureAccountPaths(cfg, account)
	if account.Disabled {
		return false, "disabled"
	}
	if !accountHasUsableArtifacts(cfg, account) {
		return false, "missing_artifacts"
	}
	// 失败冷却：被标记账号在冷却期内自动跳过，冷却到期自动恢复尝试
	if accountCooldownActive(account, now) {
		return false, "cooldown"
	}
	return true, "ready"
}

func computeAccountCooldown(account NotionAccount, retryable bool) time.Duration {
	failures := account.ConsecutiveFailures
	if failures < 1 {
		failures = 1
	}
	wait := time.Duration(failures) * accountCooldownBase
	if !retryable {
		wait /= 2
	}
	if wait < 30*time.Second {
		wait = 30 * time.Second
	}
	if wait > accountCooldownMax {
		wait = accountCooldownMax
	}
	return wait
}

func markAccountDispatchStart(account NotionAccount, now time.Time) NotionAccount {
	resetAccountUsageWindow(&account, now)
	if account.HourlyQuota > 0 {
		if strings.TrimSpace(account.WindowStartedAt) == "" {
			account.WindowStartedAt = formatRFC3339OrEmpty(now)
		}
		account.WindowRequestCount++
	}
	account.LastUsedAt = formatRFC3339OrEmpty(now)
	if strings.TrimSpace(account.Status) == "" || strings.EqualFold(account.Status, "new") {
		account.Status = "ready"
	}
	return account
}

func markAccountDispatchSuccess(account NotionAccount, now time.Time) NotionAccount {
	account.Status = "ready"
	account.LastError = ""
	account.LastUsedAt = formatRFC3339OrEmpty(now)
	account.LastSuccessAt = formatRFC3339OrEmpty(now)
	account.CooldownUntil = ""
	account.ConsecutiveFailures = 0
	account.TotalSuccesses++
	return account
}

func markAccountDispatchFailure(account NotionAccount, now time.Time, err error, retryable bool) NotionAccount {
	account.TotalFailures++
	account.ConsecutiveFailures++
	account.LastUsedAt = formatRFC3339OrEmpty(now)
	account.LastError = strings.TrimSpace(err.Error())
	if !strings.EqualFold(strings.TrimSpace(account.Status), "pending_code") {
		if retryable {
			account.Status = "expired"
		} else {
			account.Status = "failed"
		}
	}
	// 失败后进入冷却：下一次 dispatch 自动跳过该账号（到期自动恢复）
	account.CooldownUntil = now.Add(computeAccountCooldown(account, retryable)).Format(time.RFC3339)
	return account
}

func markAccountReloginPending(account NotionAccount, now time.Time) NotionAccount {
	account.Status = "pending_code"
	account.LastReloginAt = formatRFC3339OrEmpty(now)
	return account
}

func accountReloginRecentlyStarted(account NotionAccount, now time.Time) bool {
	last := parseOptionalRFC3339(account.LastReloginAt)
	return !last.IsZero() && now.Sub(last) < accountAutoReloginInterval
}

func sortDispatchCandidates(cfg AppConfig, accounts []NotionAccount, now time.Time) {
	activeKey := canonicalEmailKey(cfg.ActiveAccount)
	sort.Slice(accounts, func(i, j int) bool {
		left := accounts[i]
		right := accounts[j]
		leftKey := getAccountEmailKey(left)
		rightKey := getAccountEmailKey(right)
		leftActive := leftKey == activeKey
		rightActive := rightKey == activeKey
		if leftActive != rightActive {
			return leftActive
		}
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		leftRemaining, leftLimited := accountRemainingQuota(left, now)
		rightRemaining, rightLimited := accountRemainingQuota(right, now)
		if leftLimited != rightLimited {
			return !leftLimited
		}
		if leftLimited && rightLimited && leftRemaining != rightRemaining {
			return leftRemaining > rightRemaining
		}
		if left.ConsecutiveFailures != right.ConsecutiveFailures {
			return left.ConsecutiveFailures < right.ConsecutiveFailures
		}
		leftUsed := parseOptionalRFC3339(left.LastUsedAt)
		rightUsed := parseOptionalRFC3339(right.LastUsedAt)
		if leftUsed.IsZero() != rightUsed.IsZero() {
			return leftUsed.IsZero()
		}
		if !leftUsed.Equal(rightUsed) {
			return leftUsed.Before(rightUsed)
		}
		return leftKey < rightKey
	})
}

func buildDispatchCandidateOrder(cfg AppConfig, now time.Time) []NotionAccount {
	candidates := make([]NotionAccount, 0, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		account = ensureAccountPaths(cfg, account)
		if ok, _ := accountDispatchEligible(cfg, account, now); ok {
			candidates = append(candidates, account)
		}
	}
	sortDispatchCandidates(cfg, candidates, now)
	return candidates
}

func pickDispatchCandidatesFromSnapshot(bundle *snapshotBundle, now time.Time) []NotionAccount {
	if bundle == nil {
		return nil
	}
	if len(bundle.DispatchOrder) > 0 {
		return bundle.DispatchOrder
	}
	return buildDispatchCandidateOrder(bundle.Config, now)
}

func applyAccountUpdate(cfg AppConfig, account NotionAccount, makeActive bool) AppConfig {
	account = ensureAccountPaths(cfg, account)
	cfg.UpsertAccount(account)
	if makeActive {
		cfg.ActiveAccount = account.Email
		cfg.ProbeJSON = account.ProbeJSON
	}
	return cfg
}

func (s *ServerState) startAutoRelogin(ctx context.Context, cfg AppConfig, account NotionAccount, reason string) (AppConfig, error) {
	now := time.Now()
	account = ensureAccountPaths(cfg, account)
	if strings.TrimSpace(account.Email) == "" {
		return cfg, fmt.Errorf("account email missing for auto relogin")
	}
	if accountReloginRecentlyStarted(account, now) {
		return cfg, fmt.Errorf("auto relogin already started recently for %s", account.Email)
	}
	// P1-6 修复：登录流（发验证码邮件等）用脱离请求生命周期的独立 ctx，
	// 请求 60s 预算到期/客户端断连不再腰斩登录（否则 pending 标记已写但流程没走完，
	// 5min 内又被 accountReloginRecentlyStarted 挡住，账号长期卡 expired）。
	loginCtx, cancel := context.WithTimeout(context.Background(), helperTimeout(cfg)+30*time.Second)
	defer cancel()
	status, err := StartEmailLogin(loginCtx, cfg, LoginStartRequest{
		Email:            account.Email,
		ProfileDir:       account.ProfileDir,
		PendingPath:      account.PendingStatePath,
		StorageStatePath: account.StorageStatePath,
		AccountEmail:     account.Email,
	})
	account = mergeAccountWithStatus(cfg, account, status)
	// P0-4 联动:P1-6 的 pending 标记本来随 dispatch 失败路径顺带落盘,改 MutateAccount
	// 后该路径以"最新账号"基线记账会丢失此标记 → 重登状态必须即时、显式持久化
	if err != nil {
		// P1-6 修复：启动失败不写 LastReloginAt，下个请求可立即重试
		// （原来失败也打 pending 标记 → 5min 内重试全部被挡）
		account.LastError = firstNonEmpty(status.Error, status.Message, err.Error())
		cfg = applyAccountUpdate(cfg, account, false)
		if _, perr := s.MutateAccount(account.Email, false, func(cur NotionAccount) NotionAccount {
			cur.LastError = account.LastError
			return cur
		}); perr != nil {
			log.Printf("[auto-relogin] persist error state for %s failed: %v", account.Email, perr)
		}
		return cfg, fmt.Errorf("auto relogin start failed for %s (%s): %w", account.Email, reason, err)
	}
	account = markAccountReloginPending(account, now)
	account.LastError = ""
	cfg = applyAccountUpdate(cfg, account, false)
	if _, perr := s.MutateAccount(account.Email, false, func(cur NotionAccount) NotionAccount {
		cur.LastReloginAt = account.LastReloginAt
		cur.PendingStatePath = account.PendingStatePath
		cur.StorageStatePath = account.StorageStatePath
		cur.Status = account.Status
		cur.LastError = ""
		return cur
	}); perr != nil {
		log.Printf("[auto-relogin] persist pending state for %s failed: %v", account.Email, perr)
	}
	return cfg, fmt.Errorf("verification code required for %s; auto relogin started (%s)", account.Email, reason)
}

func (a *App) runPromptWithSession(ctx context.Context, cfg AppConfig, session SessionInfo, accountEmail string, request PromptRunRequest, onDelta func(string) error) (InferenceResult, error) {
	if a.runPromptWithSessionOverride != nil {
		return a.runPromptWithSessionOverride(ctx, cfg, session, request, onDelta)
	}
	transportClientNewTotalMetric.Add("standard", 1)
	client := newNotionAIClient(session, cfg, accountEmail)
	if onDelta != nil {
		transportClientNewTotalMetric.Add("streaming", 1)
		client = newNotionAIStreamingClient(session, cfg, accountEmail)
	}
	execute := func(ctx context.Context, current PromptRunRequest, forward func(string) error) (InferenceResult, error) {
		if forward == nil {
			return client.RunPrompt(ctx, current)
		}
		return client.RunPromptStream(ctx, current, forward)
	}
	sink := InferenceStreamSink{}
	if onDelta != nil {
		sink = InferenceStreamSink{Text: onDelta}
	}
	return a.executePromptWithRotation(ctx, cfg, session, accountEmail, request, sink, execute)
}

func (a *App) runPromptWithSessionWithSink(ctx context.Context, cfg AppConfig, session SessionInfo, accountEmail string, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
	if a.runPromptWithSessionSinkOverride != nil {
		return a.runPromptWithSessionSinkOverride(ctx, cfg, session, request, sink)
	}
	if a.runPromptWithSessionOverride != nil {
		return a.runPromptWithSessionOverride(ctx, cfg, session, request, sink.Text)
	}
	transportClientNewTotalMetric.Add("streaming", 1)
	client := newNotionAIStreamingClient(session, cfg, accountEmail)
	if sink.Text == nil && sink.Reasoning == nil && sink.ReasoningWarmup == nil && sink.KeepAlive == nil {
		transportClientNewTotalMetric.Add("standard", 1)
		client = newNotionAIClient(session, cfg, accountEmail)
	}
	if sink.Reasoning != nil || sink.ReasoningWarmup != nil || sink.KeepAlive != nil {
		client = newNotionAIStreamingClient(session, cfg, accountEmail)
	}
	execute := func(ctx context.Context, current PromptRunRequest, forward func(string) error) (InferenceResult, error) {
		if forward == nil {
			return client.RunPrompt(ctx, current)
		}
		return client.RunPromptStreamWithSink(ctx, current, InferenceStreamSink{
			Text:            forward,
			Reasoning:       sink.Reasoning,
			ReasoningWarmup: sink.ReasoningWarmup,
			KeepAlive:       sink.KeepAlive,
		})
	}
	return a.executePromptWithRotation(ctx, cfg, session, accountEmail, request, sink, execute)
}

// executePromptWithRotation — 执行 + 限制类错自动轮换重试（最多一次）
// 触发：quota-exhausted / temporarily-unavailable / errAccountStarved（CORE_PRINCIPLES §4 用户拍板）
// 轮换入口的唯一实现（dispatch 层的重复/死分支已删除，2026 审计 P0-3）。
// 流式护栏（P1-1/P1-5 修复）：执行期间已向客户端吐出任何内容（正文/推理）则不再轮换重播，
// 原样上抛错误——否则客户端会收到「前半段 + 重复完整回答」。
func (a *App) executePromptWithRotation(ctx context.Context, cfg AppConfig, session SessionInfo, accountEmail string, request PromptRunRequest, sink InferenceStreamSink, execute func(context.Context, PromptRunRequest, func(string) error) (InferenceResult, error)) (InferenceResult, error) {
	emitted := false
	guarded := sink
	if sink.Text != nil {
		inner := sink.Text
		guarded.Text = func(delta string) error {
			if delta != "" {
				emitted = true
			}
			return inner(delta)
		}
	}
	if sink.Reasoning != nil {
		inner := sink.Reasoning
		guarded.Reasoning = func(delta string) error {
			if delta != "" {
				emitted = true
			}
			return inner(delta)
		}
	}
	result, err := execute(ctx, request, guarded.Text)
	if err == nil || a.rotator == nil || !IsRotationWorthyError(err) {
		return result, err
	}
	if emitted {
		// 已吐出内容：轮换重播会造成重复输出，直接上抛原始错误（dispatch 层按 emittedAny 处理）
		return result, err
	}
	log.Printf("[workspace_rotation] quota-exhausted detected account=%s space=%s -> rotating", accountEmail, session.SpaceID)
	a.rotator.MarkExhausted(session.SpaceID, accountEmail)
	newSession, rotateErr := a.rotator.Rotate(ctx, cfg, session)
	if rotateErr != nil {
		log.Printf("[workspace_rotation] rotate failed for %s: %v", accountEmail, rotateErr)
		return result, err
	}
	log.Printf("[workspace_rotation] rotated account=%s new_space=%s retrying", accountEmail, newSession.SpaceID)
	client2 := newNotionAIClient(newSession, cfg, accountEmail)
	if sink.Text != nil || sink.Reasoning != nil || sink.ReasoningWarmup != nil || sink.KeepAlive != nil {
		client2 = newNotionAIStreamingClient(newSession, cfg, accountEmail)
	}
	execute2 := func(ctx context.Context, current PromptRunRequest, forward func(string) error) (InferenceResult, error) {
		if forward == nil {
			return client2.RunPrompt(ctx, current)
		}
		return client2.RunPromptStreamWithSink(ctx, current, InferenceStreamSink{
			Text:            guarded.Text,
			Reasoning:       guarded.Reasoning,
			ReasoningWarmup: guarded.ReasoningWarmup,
			KeepAlive:       guarded.KeepAlive,
		})
	}
	return execute2(ctx, request, guarded.Text)
}
