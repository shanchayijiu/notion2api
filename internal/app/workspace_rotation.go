package app

// workspace_rotation.go — 工作空间轮换引擎（P1 核心）
//
// 依据 research/workspace_rotation.md 的实测协议：
//   validateusercancreateworkspace → createspace(personal) → saveTransactionsMain 绑定
//   → syncRecordValuesMain 确认 → 新空间额度直接用 → quota-exhausted 触发轮换 → deleteSpace 软删
//
// 硬规则（CORE_PRINCIPLES.md）：真实创删工作空间无任何限制；发现不能无限创删 = 项目/操作问题。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	spaceStatusActive    = "active"
	spaceStatusExhausted = "exhausted"
	spaceStatusToDelete  = "to_delete"

	rotateDefaultPlanType = "personal"
	// 保守节流：单账号创建 ≥10 分钟一次（贴近真实用户频率；批量触发账号级 429 风控）
	rotateMinInterval = 10 * time.Minute
	// rotateHTTPTimeout — 轮换内部 HTTP 独立超时（createspace 响应可能 30-90s，不受请求 60s 预算限制）
	rotateHTTPTimeout = 60 * time.Second // 2026-08-26 B1：429 立即返回后不再需要 180s；60s 覆盖正常 createspace 30-90s 慢响应
)

// SpaceLifecycle — 空间生命周期记录（SQLite）
type SpaceLifecycle struct {
	SpaceID      string
	AccountEmail string
	Status       string
	ThreadID     string
	CreatedAt    string
	UpdatedAt    string
}

// WorkspaceRotator — 工作空间轮换引擎
type WorkspaceRotator struct {
	store *SQLiteStore
}

// NewWorkspaceRotator 构造轮换引擎。store 为 nil 时只做 HTTP 操作不持久化。
func NewWorkspaceRotator(store *SQLiteStore) *WorkspaceRotator {
	return &WorkspaceRotator{store: store}
}

// IsQuotaExhaustedError — 判断推理错误是否为额度耗尽（NDJSON record-map subType="quota-exhausted"）
func IsQuotaExhaustedError(err error) bool {
	if err == nil {
		return false
	}
	var stepErr *inferenceStepError
	if !errors.As(err, &stepErr) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(stepErr.SubType), "quota-exhausted")
}

// isTemporarilyUnavailableError — 上游暂时不可用（subType="temporarily-unavailable"）
func isTemporarilyUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var stepErr *inferenceStepError
	if !errors.As(err, &stepErr) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(stepErr.SubType), "temporarily-unavailable")
}

// IsRotationWorthyError — 限制类错是否值得走工作空间轮换（CORE_PRINCIPLES §4 用户拍板）：
// 额度耗尽 / 暂时不可用 / 账号级 starve（未启动推理）→ 新建空间续聊，不固定冷却、不换号。
func IsRotationWorthyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errAccountStarved) {
		return true
	}
	return IsQuotaExhaustedError(err) || isTemporarilyUnavailableError(err)
}

// Rotate — 换新工作空间：createSpace + 绑定 + 更新 session（会话上下文由上层重建）
// 内置保守节流：单账号两次创建间隔 ≥ rotateMinInterval（贴近真实频率，防账号级 429）
// 号冷却（用户 2026-08-24 拍板）：429/每日上限 → 标记账号 cooldown +24h（每日重置），
// 不删号；冷却过期自动恢复可用。
func (r *WorkspaceRotator) Rotate(ctx context.Context, cfg AppConfig, session SessionInfo) (SessionInfo, error) {
	accountEmail := strings.TrimSpace(session.ProbePath)
	if accountEmail == "" {
		accountEmail = findAccountEmailForSession(cfg, session)
	}
	email := strings.TrimSpace(session.UserEmail)
	if email == "" {
		email = accountEmail
	}
	// 账号每日冷却检查（429 后标记，24h 自动恢复）
	if active, until := accountDailyCooldownActive(cfg, email); active {
		return session, fmt.Errorf("workspace rotation: account cooling down until %s (daily limit; auto recovers)", until.Format(time.RFC3339))
	}
	if r.store != nil {
		recent, err := r.store.LoadSpaceLifecycles(email, spaceStatusActive)
		if err == nil && len(recent) > 0 {
			if last := parseLifecycleTime(recent[0].CreatedAt); !last.IsZero() && time.Since(last) < rotateMinInterval {
				return session, fmt.Errorf("workspace rotation throttled: last create at %s, min interval %s",
					last.Format(time.RFC3339), rotateMinInterval)
			}
		}
	}
	client := newNotionAIClient(session, cfg, accountEmail)

	// 轮换是恢复动作，用独立更长超时（不继承请求 60s 预算；createspace 响应可能 30-90s）
	rotateCtx, cancel := context.WithTimeout(context.Background(), rotateHTTPTimeout)
	defer cancel()

	// 1) 前哨
	validateBody, err := client.postJSON(rotateCtx, client.Config.NotionUpstream().API("validateusercancreateworkspace"),
		map[string]any{}, "application/json")
	if err != nil {
		return session, fmt.Errorf("validateusercancreateworkspace: %w", err)
	}
	var vj struct {
		CanUserCreateSpace bool `json:"canUserCreateSpace"`
	}
	_ = json.Unmarshal(validateBody, &vj)
	if !vj.CanUserCreateSpace {
		return session, fmt.Errorf("validateusercancreateworkspace rejected: canUserCreateSpace=false body=%s",
			truncateBytes(validateBody, 200))
	}

	// 2) createspace（free 账号必须 personal；team → 504 慢路径）
	deviceID := resolveAccountDeviceID(cfg, session)
	spaceID, viewID, err := r.createSpaceHTTP(rotateCtx, client, deviceID)
	if err != nil {
		// 429 = 号冷却/每日上限：标记账号 cooldown +24h（每日重置，不删号）
		var apiErr *notionAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			if r.markAccountDailyCooldown(cfg, email, 24*time.Hour) {
				log.Printf("[workspace_rotation] account %s daily-create-limit; cooldown 24h set (auto recovers)", email)
			}
		}
		return session, fmt.Errorf("createspace: %w", err)
	}

	// 3) 绑定：createSpaceView:true 时服务端已建 view（响应带 spaceViewPointer）；
	//    缺失才手动绑定（旧路径，防幽灵空间 trap）
	if strings.TrimSpace(viewID) == "" {
		viewID, err = r.bindSpaceHTTP(rotateCtx, client, spaceID)
		if err != nil {
			return session, fmt.Errorf("bind space %s: %w", spaceID, err)
		}
	} else {
		r.recordLifecycle(spaceID, session.UserEmail, spaceStatusActive, "")
	}

	// 4) 更新 session 指向新空间
	session.SpaceID = spaceID
	session.SpaceViewID = viewID
	// 4.5) 新空间写回 probe.json（2026-08-25 实锤：不落盘则 dispatch 重启后仍用旧 space_id → 推理失败）
	// client 在空间创建前创建，持有旧 session——必须先同步再持久化
	client.Session.SpaceID = spaceID
	client.Session.SpaceViewID = viewID
	if err := client.persistSessionProbe(); err != nil {
		log.Printf("[workspace_rotation] persist probe failed for %s: %v", email, err)
	} else {
		log.Printf("[workspace_rotation] probe persisted space_id=%s view=%s", spaceID, viewID)
	}

	// 5) 生命周期记录
	r.recordLifecycle(spaceID, session.UserEmail, spaceStatusActive, "")

	return session, nil
}

// MarkExhausted — 额度耗尽登记（SQLite status=exhausted）
func (r *WorkspaceRotator) MarkExhausted(spaceID string, accountEmail string) {
	if r == nil || r.store == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	_ = r.store.UpdateSpaceLifecycleStatus(spaceID, spaceStatusExhausted)
}

// MarkToDelete — 标记旧空间可删（P2 删除执行器启用后生效）
func (r *WorkspaceRotator) MarkToDelete(spaceID string) {
	if r == nil || r.store == nil || strings.TrimSpace(spaceID) == "" {
		return
	}
	_ = r.store.UpdateSpaceLifecycleStatus(spaceID, spaceStatusToDelete)
}

// DeleteSpace — 软删旧空间（异步，outputKey 无轮询入口；以 sync 结果为准）
func (r *WorkspaceRotator) DeleteSpace(ctx context.Context, cfg AppConfig, session SessionInfo, spaceID string) error {
	client := newNotionAIClient(session, cfg, session.UserEmail)
	body, err := client.postJSON(ctx, client.Config.NotionUpstream().API("deleteSpace"),
		map[string]any{"spaceId": spaceID}, "application/json")
	if err != nil {
		return fmt.Errorf("deleteSpace %s: %w", spaceID, err)
	}
	var dj struct {
		OutputKey string `json:"outputKey"`
	}
	_ = json.Unmarshal(body, &dj)
	if dj.OutputKey == "" {
		return fmt.Errorf("deleteSpace %s: unexpected response %s", spaceID, truncateBytes(body, 200))
	}
	r.MarkToDelete(spaceID)
	return nil
}

// createSpaceHTTP — POST /api/v3/createspace
// 2026-08-24 最终确认（Cloak UI 抓取真实请求 + 协议版 200 验证）：
//   精确 body = name/icon("🏠")/planType/planSelection/initialPersona/deviceId/deviceType/source/createSpaceView
//   deviceId 必须 = cookies 的 notion_browser_id
//   createSpaceView:true → 服务端直接建 view（响应带 spaceViewPointer，无需手动绑定）
//   429 = 号被非标准请求标记后的拒绝（标记后该号创建全挂；干净号+精确配方正常）
func (r *WorkspaceRotator) createSpaceHTTP(ctx context.Context, client *NotionAIClient, deviceID string) (string, string, error) {
	if strings.TrimSpace(deviceID) == "" {
		deviceID = randomUUID()
	}
	payload := map[string]any{
		"name":            rotateSpaceName(),
		"icon":            "🏠",
		"planType":        rotateDefaultPlanType,
		"planSelection":   rotateDefaultPlanType,
		"initialPersona":  "unfilled",
		"deviceId":        deviceID,
		"deviceType":      "web-desktop",
		"source":          "handle_root_redirect",
		"createSpaceView": true,
	}
	var lastErr error
	// 429 换 deviceId 复位（CORE_PRINCIPLES §1 第 2 条 + 研究 2026-08-23：
	// 号被标记后换 notion_browser_id 可复位 spam heuristic）——先换 ID 重试，仍 429 才退避
	deviceIDReset := false
	for attempt := 1; attempt <= 3; attempt++ {
		body, err := client.postJSONWithReferer(ctx,
			client.Config.NotionUpstream().API("createspace"), payload,
			"application/json", client.Config.NotionUpstream().BaseURL+"/onboarding")
		if err == nil {
			var cs struct {
				SpaceID          string `json:"spaceId"`
				SpaceViewPointer *struct {
					ID string `json:"id"`
				} `json:"spaceViewPointer"`
			}
			if uerr := json.Unmarshal(body, &cs); uerr != nil {
				return "", "", fmt.Errorf("createspace parse: %w body=%s", uerr, truncateBytes(body, 200))
			}
			if cs.SpaceID != "" {
				viewID := ""
				if cs.SpaceViewPointer != nil {
					viewID = cs.SpaceViewPointer.ID
				}
				return cs.SpaceID, viewID, nil
			}
			lastErr = fmt.Errorf("createspace no spaceId: %s", truncateBytes(body, 200))
			break
		}
		lastErr = err
		var apiErr *notionAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			if !deviceIDReset {
				// 首次 429：换 notion_browser_id + deviceId 复位后立即重试（绕过 spam heuristic）
				deviceIDReset = true
				newID := randomUUID()
				deviceID = newID
				payload["deviceId"] = newID
				for i := range client.Session.Cookies {
					if strings.TrimSpace(client.Session.Cookies[i].Name) == "notion_browser_id" {
						client.Session.Cookies[i].Value = newID
					}
				}
				log.Printf("[workspace_rotation] createspace 429 -> deviceId reset (%s), retrying", newID[:8])
				continue
			}
			// 2026-08-26 B1 熔断：deviceId 复位后仍 429 = 每日上限，立即返回
			// （此前退避 5-15min > rotateHTTPTimeout → 超时错误掩盖 429 → 冷却不触发 → 整日死锁 502）
			log.Printf("[workspace_rotation] createspace 429 persists after deviceId reset; returning 429 (account cooldown 24h)")
			return "", "", err
		}
		if errors.As(err, &apiErr) && apiErr.StatusCode < 500 {
			break
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return "", "", lastErr
}

func spaceDomainFromEmail(email string) string {
	email = strings.TrimSpace(email)
	if at := strings.LastIndex(email, "@"); at >= 0 && at < len(email)-1 {
		return email[at+1:]
	}
	return ""
}

// accountDailyCooldownActive — 账号每日冷却检查（429 后 +24h，自动恢复）
func accountDailyCooldownActive(cfg AppConfig, accountEmail string) (bool, time.Time) {
	for _, acc := range cfg.Accounts {
		if strings.EqualFold(strings.TrimSpace(acc.Email), strings.TrimSpace(accountEmail)) {
			until := parseOptionalRFC3339(acc.CooldownUntil)
			if !until.IsZero() && time.Now().Before(until) {
				return true, until
			}
			return false, time.Time{}
		}
	}
	return false, time.Time{}
}

// markAccountDailyCooldown — 标记账号冷却（不删号；冷却过期自动恢复）
func (r *WorkspaceRotator) markAccountDailyCooldown(cfg AppConfig, accountEmail string, d time.Duration) bool {
	if r == nil || r.store == nil {
		return false
	}
	for i := range cfg.Accounts {
		if strings.EqualFold(strings.TrimSpace(cfg.Accounts[i].Email), strings.TrimSpace(accountEmail)) {
			cfg.Accounts[i].CooldownUntil = time.Now().Add(d).Format(time.RFC3339)
			cfg.Accounts[i].LastError = "daily workspace create limit reached (auto recovers)"
			if err := r.store.SaveAccounts(cfg); err != nil {
				log.Printf("[workspace_rotation] save cooldown for %s failed: %v", accountEmail, err)
			}
			return true
		}
	}
	return false
}

// bindSpaceHTTP — saveTransactionsMain 绑定 + syncRecordValuesMain 轮询确认（三操作见 capture entry 41）
func (r *WorkspaceRotator) bindSpaceHTTP(ctx context.Context, client *NotionAIClient, spaceID string) (string, error) {
	userID := client.Session.UserID
	viewID := randomUUID()
	nowMS := fmt.Sprintf("%d", time.Now().UnixMilli())
	body := map[string]any{
		"requestId": randomUUID(),
		"transactions": []any{map[string]any{
			"id":      randomUUID(),
			"spaceId": spaceID,
			"debug":   map[string]any{"userAction": "spaceActions.createSpace"},
			"operations": []any{
				map[string]any{
					"pointer": map[string]any{"table": "space_view", "id": viewID, "spaceId": spaceID},
					"path":    []any{}, "command": "set",
					"args": map[string]any{
						"id": viewID, "version": 1, "space_id": spaceID,
						"notify_mobile": true, "notify_desktop": true, "notify_email": true,
						"parent_id": userID, "parent_table": "user_root", "alive": true,
						"first_joined_space_time": nowMS, "joined": true,
						"settings": map[string]any{"notify_email_digest": true,
							"notify_home_digest_email": true},
					},
				},
				map[string]any{
					"pointer": map[string]any{"table": "user_root", "id": userID},
					"path":    []any{"space_views"}, "command": "listAfter",
					"args": map[string]any{"id": viewID},
				},
				map[string]any{
					"pointer": map[string]any{"table": "user_root", "id": userID},
					"path":    []any{"space_view_pointers"}, "command": "keyedObjectListAfter",
					"args": map[string]any{"value": map[string]any{
						"table": "space_view", "id": viewID, "spaceId": spaceID}},
				},
			},
		}},
	}
	raw, err := client.postJSON(ctx, client.Config.NotionUpstream().API("saveTransactionsMain"), body, "application/json")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(raw)) != "" && strings.TrimSpace(string(raw)) != "{}" {
		// 非空非 {} 的响应可能是错误
		var ej struct {
			IsNotionError bool   `json:"isNotionError"`
			Message       string `json:"message"`
		}
		_ = json.Unmarshal(raw, &ej)
		if ej.IsNotionError {
			return "", fmt.Errorf("saveTransactionsMain rejected: %s", ej.Message)
		}
	}

	// syncRecordValuesMain 轮询 user_root.space_view_pointers（容忍偶发空返回，重试）
	pollPayload := map[string]any{"requests": []any{map[string]any{
		"pointer": map[string]any{"table": "user_root", "id": userID}, "version": -1}}}
	for round := 0; round < 6; round++ {
		pollRaw, perr := client.postJSON(ctx,
			client.Config.NotionUpstream().API("syncRecordValuesMain"), pollPayload, "application/json")
		if perr == nil {
			var pr struct {
				RecordMap map[string]map[string]struct {
					Value *struct {
						Value *struct {
							SpaceViewPointers *struct {
								Value []struct {
									SpaceID string `json:"spaceId"`
								} `json:"value"`
							} `json:"space_view_pointers"`
						} `json:"value"`
					} `json:"value"`
				} `json:"recordMap"`
			}
			if uerr := json.Unmarshal(pollRaw, &pr); uerr == nil {
				if ur, ok := pr.RecordMap["user_root"][userID]; ok && ur.Value != nil && ur.Value.Value != nil &&
					ur.Value.Value.SpaceViewPointers != nil {
					for _, p := range ur.Value.Value.SpaceViewPointers.Value {
						if p.SpaceID == spaceID {
							return viewID, nil
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("space_view binding not confirmed after polls (ghost space); space_id=%s", spaceID)
}

// recordLifecycle — 空间生命周期写 SQLite
func (r *WorkspaceRotator) recordLifecycle(spaceID string, accountEmail string, status string, threadID string) {
	if r == nil || r.store == nil {
		return
	}
	_ = r.store.SaveSpaceLifecycle(SpaceLifecycle{
		SpaceID: spaceID, AccountEmail: accountEmail,
		Status: status, ThreadID: threadID,
	})
}

func rotateSpaceName() string {
	return fmt.Sprintf("ws-%s", randomUUID()[:8])
}

func resolveAccountDeviceID(cfg AppConfig, session SessionInfo) string {
	// 2026-08-23 情报：createspace 的 deviceId 必须 = cookies 里的 notion_browser_id
	//（前端 getExperimentDeviceId 读它）；用错 deviceId 触发账号级 429 风控。
	for _, c := range session.Cookies {
		if strings.TrimSpace(c.Name) == "notion_browser_id" {
			if v := strings.TrimSpace(c.Value); v != "" {
				return v
			}
		}
	}
	for _, acc := range cfg.Accounts {
		if strings.EqualFold(strings.TrimSpace(acc.Email), strings.TrimSpace(session.UserEmail)) ||
			(acc.ProbeJSON != "" && strings.EqualFold(normalizePath(acc.ProbeJSON), normalizePath(session.ProbePath))) {
			if strings.TrimSpace(acc.DeviceID) != "" {
				return acc.DeviceID
			}
		}
	}
	return ""
}

func findAccountEmailForSession(cfg AppConfig, session SessionInfo) string {
	if strings.TrimSpace(session.UserEmail) != "" {
		return session.UserEmail
	}
	for _, acc := range cfg.Accounts {
		if acc.ProbeJSON != "" && normalizePath(acc.ProbeJSON) == normalizePath(session.ProbePath) {
			return acc.Email
		}
	}
	return ""
}

func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSpace(p)
	return strings.ToLower(p)
}

func truncateBytes(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func parseLifecycleTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err == nil {
		return t
	}
	t, err = time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err == nil {
		return t
	}
	return time.Time{}
}