package app

// stability_regression_test.go — 2026 稳定性审计 P0/P1 修复的回归测试
// 覆盖:半开探测(P0-1)、pinning fallback(P0-2)、容量错不记账(P1-2)、
//      NDJSON 静默看门狗(stream P0-1)、anthropic 空成功消息(P1-1)、shortID 安全

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── 工具 ──────────────────────────────────────────────────────────────

func testConfigWithAccounts(accounts ...NotionAccount) AppConfig {
	return AppConfig{Accounts: accounts}
}

func accountWithProbe(t *testing.T, email string, mutate func(*NotionAccount)) NotionAccount {
	t.Helper()
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe.json")
	if err := os.WriteFile(probe, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	acc := NotionAccount{Email: email, ProbeJSON: probe}
	if mutate != nil {
		mutate(&acc)
	}
	return acc
}

// ── dispatch P0-1:半开探测 ────────────────────────────────────────────

func TestResolveDispatchCandidates_AllCooling_ReturnsSentinel(t *testing.T) {
	// 全员冷却 → 返回哨兵错误(由 dispatch 捕获并走半开),而非静默空列表
	now := time.Now()
	cooling := now.Add(10 * time.Minute).Format(time.RFC3339)
	cfg := testConfigWithAccounts(
		accountWithProbe(t, "a@x.com", func(a *NotionAccount) { a.CooldownUntil = cooling }),
		accountWithProbe(t, "b@x.com", func(a *NotionAccount) { a.CooldownUntil = cooling }),
	)
	_, err := resolveDispatchCandidates(cfg, PromptRunRequest{}, now)
	if !errors.Is(err, errNoEligibleAccounts) {
		t.Fatalf("expect errNoEligibleAccounts, got %v", err)
	}
}

func TestBuildHalfOpenCandidates_SkipsDisabled_SortsByCooldownExpiry(t *testing.T) {
	now := time.Now()
	early := now.Add(5 * time.Minute).Format(time.RFC3339)
	late := now.Add(25 * time.Minute).Format(time.RFC3339)
	cfg := testConfigWithAccounts(
		accountWithProbe(t, "disabled@x.com", func(a *NotionAccount) { a.Disabled = true; a.CooldownUntil = early }),
		accountWithProbe(t, "late@x.com", func(a *NotionAccount) { a.CooldownUntil = late }),
		accountWithProbe(t, "early@x.com", func(a *NotionAccount) { a.CooldownUntil = early }),
		NotionAccount{Email: "noartifacts@x.com"}, // 无制品 → 排除
	)
	half := buildHalfOpenCandidates(cfg)
	if len(half) != 2 {
		t.Fatalf("expect 2 half-open candidates (skip disabled + no-artifacts), got %d", len(half))
	}
	if half[0].Email != "early@x.com" || half[1].Email != "late@x.com" {
		t.Fatalf("expect cooldown-expiry ascending order, got %s,%s", half[0].Email, half[1].Email)
	}
}

// ── dispatch P0-2:续聊 pinning fallback ──────────────────────────────

func TestPinnedAccount_CoolingWithFallback_FallsToHealthyPoolAccount(t *testing.T) {
	now := time.Now()
	cooling := now.Add(10 * time.Minute).Format(time.RFC3339)
	cfg := testConfigWithAccounts(
		accountWithProbe(t, "dead@x.com", func(a *NotionAccount) { a.CooldownUntil = cooling }),
		accountWithProbe(t, "healthy@x.com", nil),
	)
	req := PromptRunRequest{PinnedAccountEmail: "dead@x.com", AllowPinnedAccountFallback: true}
	candidates, err := resolveDispatchCandidates(cfg, req, now)
	if err != nil {
		t.Fatalf("fallback should not error, got %v", err)
	}
	if len(candidates) != 1 || candidates[0].Email != "healthy@x.com" {
		t.Fatalf("expect only healthy account, got %+v", candidates)
	}
}

func TestPinnedAccount_StrictDisabled_HardError(t *testing.T) {
	cfg := testConfigWithAccounts(
		accountWithProbe(t, "dead@x.com", func(a *NotionAccount) { a.Disabled = true }),
		accountWithProbe(t, "healthy@x.com", nil),
	)
	// 无 fallback(客户端显式指定)→ pinned disabled 应直接报错,不静默换号
	req := PromptRunRequest{PinnedAccountEmail: "dead@x.com", AllowPinnedAccountFallback: false}
	_, err := resolveDispatchCandidates(cfg, req, time.Now())
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expect disabled error, got %v", err)
	}
}

// ── dispatch P1-2:容量错不计入账号失败 ───────────────────────────────

func TestDispatchCapacityError_NotAccountFailure(t *testing.T) {
	err := noDispatchCapacityError()
	if !isDispatchCapacityExceededError(err) {
		t.Fatalf("capacity error must be detectable")
	}
	wrapped := errors.Join(err, io.EOF)
	if !isDispatchCapacityExceededError(wrapped) {
		t.Fatalf("capacity error must survive wrapping")
	}
	if isDispatchCapacityExceededError(errors.New("random")) {
		t.Fatalf("random error misdetected as capacity")
	}
}

// ── stream P0-1:NDJSON 静默看门狗 ────────────────────────────────────

type blockingReadCloser struct {
	started chan []byte
	closed  chan struct{}
}

func (b *blockingReadCloser) Read(p []byte) (int, error) {
	select {
	case data := <-b.started:
		return copy(p, data), nil
	case <-b.closed:
		return 0, io.EOF
	}
}

func (b *blockingReadCloser) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func withSilenceTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := ndjsonSilenceTimeout
	ndjsonSilenceTimeout = d
	t.Cleanup(func() { ndjsonSilenceTimeout = old })
}

func TestNDJSONSilenceWatchdog_NoLines_StarvedError(t *testing.T) {
	withSilenceTimeout(t, 80*time.Millisecond)
	reader := &blockingReadCloser{started: make(chan []byte, 4), closed: make(chan struct{})}
	start := time.Now()
	_, err := consumeNDJSONStreamWithIdleClose(reader, "thread-x", InferenceStreamSink{}, 0)
	if err == nil {
		t.Fatalf("expect watchdog error on total silence")
	}
	if !errors.Is(err, errAccountStarved) {
		t.Fatalf("silence must surface as account-level fault (errAccountStarved), got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("watchdog too slow: %v", elapsed)
	}
}

func TestNDJSONSilenceWatchdog_ScaffoldLinesOnly_StarvedError(t *testing.T) {
	// 账号被上游标记的真实形态:只回 config/context/user scaffold 行,永不推理
	withSilenceTimeout(t, 100*time.Millisecond)
	reader := &blockingReadCloser{started: make(chan []byte, 4), closed: make(chan struct{})}
	reader.started <- []byte(`{"type":"config","id":"c1"}` + "\n")
	reader.started <- []byte(`{"type":"context","id":"c2"}` + "\n")
	_, err := consumeNDJSONStreamWithIdleClose(reader, "thread-y", InferenceStreamSink{}, 0)
	if err == nil || !errors.Is(err, errAccountStarved) {
		t.Fatalf("silence after scaffold must be starved error, got %v", err)
	}
}

func TestNDJSONSilenceWatchdog_PartialAnswerThenStall_GracefulEOF(t *testing.T) {
	// 已吐出可见答案后静默 → 按 EOF 收尾(客户端保留已收内容),不算账号故障
	withSilenceTimeout(t, 100*time.Millisecond)
	reader := &blockingReadCloser{started: make(chan []byte, 4), closed: make(chan struct{})}
	reader.started <- []byte(`{"type":"agent-inference","id":"m1","value":[{"type":"text","content":"partial answer"}]}` + "\n")
	res, err := consumeNDJSONStreamWithIdleClose(reader, "thread-z", InferenceStreamSink{}, 0)
	if err != nil {
		t.Fatalf("partial answer stall should EOF gracefully, got %v", err)
	}
	if !res.HasAgentInference {
		t.Fatalf("expected agent inference recorded")
	}
}

func TestNDJSONSilenceWatchdog_HealthyStream_NotFired(t *testing.T) {
	// 持续有行 → 看门狗不触发,流正常收尾
	withSilenceTimeout(t, 50*time.Millisecond)
	reader := &blockingReadCloser{started: make(chan []byte, 4), closed: make(chan struct{})}
	reader.started <- []byte(`{"type":"agent-inference","id":"m1","value":[{"type":"text","content":"first"}],"finishedAt":"2026-01-01T00:00:00Z"}` + "\n")
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = reader.Close()
	}()
	res, err := consumeNDJSONStreamWithIdleClose(reader, "thread-w", InferenceStreamSink{}, 0)
	if err != nil {
		t.Fatalf("healthy stream must not error, got %v", err)
	}
	if strings.TrimSpace(res.FinalAgent.Text) == "" {
		t.Fatalf("expected final text from completed agent message")
	}
}

// ── P1-1:anthropic 流式上游错误不再吞成空成功 ────────────────────────

type sseRecorder struct {
	buf strings.Builder
}

func (r *sseRecorder) Header() http.Header         { return http.Header{} }
func (r *sseRecorder) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *sseRecorder) WriteHeader(int)             {}
func (r *sseRecorder) Flush()                      {}

func TestAnthropicConverter_UpstreamError_EmitsErrorEvent(t *testing.T) {
	pr, pw := io.Pipe()
	rec := &sseRecorder{}
	lw := &liveSSEWriter{pw: pw, header: http.Header{}, status: 502}
	converter := newAnthropicEventConverter(rec, rec, "msg_test", "opus-5")
	converter.upstream = lw
	go func() {
		// 内部链路在上游失败时写的是 JSON 错误体(非 data: 行)
		_, _ = pw.Write([]byte(`{"error":{"message":"no usable accounts available"}}` + "\n"))
		_ = pw.Close()
	}()
	err := converter.run(pr)
	if err == nil {
		t.Fatalf("converter must surface upstream error, got nil")
	}
	out := rec.buf.String()
	if !strings.Contains(out, "event: error") || !strings.Contains(out, "no usable accounts") {
		t.Fatalf("expect error event with upstream message, got:\n%s", out)
	}
	if strings.Contains(out, "message_start") {
		t.Fatalf("must NOT emit success skeleton on upstream error:\n%s", out)
	}
}

func TestAnthropicConverter_EmptyStream_KeepsSkeleton(t *testing.T) {
	pr, pw := io.Pipe()
	rec := &sseRecorder{}
	lw := &liveSSEWriter{pw: pw, header: http.Header{}, status: 200}
	converter := newAnthropicEventConverter(rec, rec, "msg_test2", "opus-5")
	converter.upstream = lw
	go func() { _ = pw.Close() }()
	if err := converter.run(pr); err != nil {
		t.Fatalf("empty stream without error signal must not fail, got %v", err)
	}
	out := rec.buf.String()
	if !strings.Contains(out, "message_start") || !strings.Contains(out, "message_stop") {
		t.Fatalf("expect skeleton on quiet-but-clean stream, got:\n%s", out)
	}
}

func TestAnthropicConverter_ScanError_Reported(t *testing.T) {
	// 超长行(>16MB)触发 scanner 错误 → 返回错误而非静默收尾
	pr, pw := io.Pipe()
	rec := &sseRecorder{}
	converter := newAnthropicEventConverter(rec, rec, "msg_test3", "opus-5")
	go func() {
		_, _ = pw.Write(make([]byte, 17*1024*1024)) // 无换行的 17MB 单行
		_ = pw.Close()
	}()
	if err := converter.run(pr); err == nil {
		t.Fatalf("scanner error must be reported")
	}
}

// ── P2-6:shortID 安全(不再裸切片) ─────────────────────────────────

func TestShortID_NeverPanicsAndBounded(t *testing.T) {
	for _, n := range []int{0, 1, 8, 16, 20, 64} {
		got := shortID(n)
		if n > 0 && len(got) > n {
			t.Fatalf("shortID(%d) len=%d exceeds", n, len(got))
		}
		if strings.Contains(got, "-") {
			t.Fatalf("shortID must be dash-free, got %q", got)
		}
	}
}

// ── 账号池:冷却与失败记账语义 ───────────────────────────────────────

func TestMarkAccountDispatchFailure_SetsCooldown(t *testing.T) {
	acc := NotionAccount{Email: "a@x.com"}
	acc = markAccountDispatchFailure(acc, time.Now(), errors.New("boom"), false)
	if strings.TrimSpace(acc.CooldownUntil) == "" {
		t.Fatalf("failure must set cooldown")
	}
	if acc.ConsecutiveFailures != 1 {
		t.Fatalf("failure count mismatch: %d", acc.ConsecutiveFailures)
	}
	// 成功复位
	acc = markAccountDispatchSuccess(acc, time.Now())
	if acc.CooldownUntil != "" || acc.ConsecutiveFailures != 0 {
		t.Fatalf("success must reset failure state")
	}
}

// ── P0-4:账号运行时状态并发写(lost-update 回归) ─────────────────────

func TestMutateAccount_ConcurrentNoLostUpdates(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "probe.json")
	_ = os.WriteFile(probe, []byte(`{}`), 0o644)
	acc := NotionAccount{Email: "c@x.com", ProbeJSON: probe}
	state := &ServerState{
		Config: AppConfig{APIKey: "test-key", Accounts: []NotionAccount{acc}},
	}
	state.updateSnapshotBundleLocked()

	const goroutines = 8
	done := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			// 锁内取最新值再变换:并发失败记账不丢
			_, err := state.MutateAccount("c@x.com", false, func(cur NotionAccount) NotionAccount {
				cur.ConsecutiveFailures++
				cur.CooldownUntil = time.Now().Add(time.Minute).Format(time.RFC3339)
				return cur
			})
			done <- err
		}()
	}
	for i := 0; i < goroutines; i++ {
		if err := <-done; err != nil {
			t.Fatalf("MutateAccount failed: %v", err)
		}
	}
	var final NotionAccount
	for _, a := range state.Config.Accounts {
		if a.Email == "c@x.com" {
			final = a
		}
	}
	if final.ConsecutiveFailures != goroutines {
		t.Fatalf("lost-update detected: expect %d failures, got %d", goroutines, final.ConsecutiveFailures)
	}
	if strings.TrimSpace(final.CooldownUntil) == "" {
		t.Fatalf("cooldown must be set by mutations")
	}
}

// ── 轮换收口(P0-3):limits 类错误才轮换 + 无 rotator 时透传 ──────────

func TestIsRotationWorthyError_Table(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"starved", errAccountStarved, true},
		{"starved-wrapped", fmt.Errorf("run failed: %w", errAccountStarved), true},
		{"generic", errors.New("boom"), false},
	}
	for _, tc := range cases {
		if got := IsRotationWorthyError(tc.err); got != tc.want {
			t.Fatalf("%s: expect %v, got %v", tc.name, tc.want, got)
		}
	}
}

func TestExecutePromptWithRotation_NonQuotaError_PassesThrough(t *testing.T) {
	// 非限制类错误即使发了内容也不应触发轮换;rotator 为 nil 时任何错误直接透传
	app := &App{}
	want := errors.New("random transport err")
	var emitted []string
	sink := InferenceStreamSink{Text: func(s string) error { emitted = append(emitted, s); return nil }}
	_, err := app.executePromptWithRotation(context.Background(), AppConfig{}, SessionInfo{}, "a@x.com",
		PromptRunRequest{}, sink,
		func(ctx context.Context, req PromptRunRequest, fwd func(string) error) (InferenceResult, error) {
			if fwd != nil {
				_ = fwd("partial")
			}
			return InferenceResult{}, want
		})
	if !errors.Is(err, want) {
		t.Fatalf("non-rotation error must pass through unchanged, got %v", err)
	}
	if len(emitted) != 1 || emitted[0] != "partial" {
		t.Fatalf("emission forwarding broken: %v", emitted)
	}
}

func TestMutateAccount_MakeActive(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "probe.json")
	_ = os.WriteFile(probe, []byte(`{}`), 0o644)
	state := &ServerState{Config: AppConfig{APIKey: "k", Accounts: []NotionAccount{{Email: "a@x.com", ProbeJSON: probe}, {Email: "b@x.com", ProbeJSON: probe}}}}
	if _, err := state.MutateAccount("b@x.com", true, func(cur NotionAccount) NotionAccount { cur.Status = "ready"; return cur }); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if state.Config.ActiveAccount != "b@x.com" {
		t.Fatalf("makeActive must switch active account, got %q", state.Config.ActiveAccount)
	}
	if _, err := state.MutateAccount("ghost@x.com", false, func(cur NotionAccount) NotionAccount { return cur }); err == nil {
		t.Fatalf("unknown account must error")
	}
}

// ── Phase 2:register 配置化 + 池水位巡检 + healthz 分级 ──────────────

func TestExtractRegisteredEmail(t *testing.T) {
	out := "[=== 1/1 ===] OK 成功: mt6puecq4ubt@imageeditgpt.com  space_id=abc"
	email, err := extractRegisteredEmail(out)
	if err != nil || email != "mt6puecq4ubt@imageeditgpt.com" {
		t.Fatalf("extract failed: %q %v", email, err)
	}
	if _, err := extractRegisteredEmail("nothing here"); err == nil {
		t.Fatalf("expect error on junk output")
	}
}

func TestResolveRegisterDefaults(t *testing.T) {
	cfg := AppConfig{}
	rc := cfg.ResolveRegister()
	if rc.ScriptName != "batch_run_proto.py" || rc.PythonBin != "python3" {
		t.Fatalf("defaults wrong: %+v", rc)
	}
	if rc.TimeoutSec != 180 || rc.CheckIntervalSec != 600 {
		t.Fatalf("timeout defaults wrong: %+v", rc)
	}
	// Enabled 但 MinHealthy 未配 → 默认 1
	cfg.Register.Enabled = true
	if rc2 := cfg.ResolveRegister(); rc2.MinHealthy != 1 {
		t.Fatalf("MinHealthy default while Enabled must be 1, got %d", rc2.MinHealthy)
	}
}

func TestCountHealthyAccounts(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "probe.json")
	_ = os.WriteFile(probe, []byte(`{}`), 0o644)
	cooling := time.Now().Add(time.Hour).Format(time.RFC3339)
	cfg := AppConfig{Accounts: []NotionAccount{
		{Email: "ok@x.com", ProbeJSON: probe},                           // 健康
		{Email: "cool@x.com", ProbeJSON: probe, CooldownUntil: cooling}, // 冷却中
		{Email: "off@x.com", ProbeJSON: probe, Disabled: true},          // 管理员禁用
		{Email: "bare@x.com"},                                           // 无 artifact
	}}
	h, tot := countHealthyAccounts(cfg)
	if h != 1 || tot != 4 {
		t.Fatalf("expect healthy=1 total=4, got %d/%d", h, tot)
	}
}

func TestHealthzPoolReadyField(t *testing.T) {
	body1 := appendHealthzRuntimeFields([]byte(`{"ok":true}`), true, time.Time{}, "", 0, 3)
	if !strings.Contains(string(body1), `"pool_ready":false`) {
		t.Fatalf("pool_ready must be false when 0 healthy: %s", body1)
	}
	body2 := appendHealthzRuntimeFields([]byte(`{"ok":true}`), true, time.Time{}, "", 2, 3)
	for _, want := range []string{`"pool_ready":true`, `"pool_healthy":2`, `"pool_total":3`} {
		if !strings.Contains(string(body2), want) {
			t.Fatalf("missing %s in %s", want, body2)
		}
	}
}

// ── /internal/metrics:expvar 暴露 + API Key 保护 ─────────────────────

func TestInternalMetricsEndpoint(t *testing.T) {
	state := &ServerState{Config: AppConfig{APIKey: "k"}}
	a := &App{State: state}

	// 无 key → 401
	req1 := httptest.NewRequest(http.MethodGet, "http://x/internal/metrics", nil)
	rr1 := httptest.NewRecorder()
	a.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusUnauthorized {
		t.Fatalf("no-key must be 401, got %d", rr1.Code)
	}

	// 有 key → 200 + expvar payload
	req2 := httptest.NewRequest(http.MethodGet, "http://x/internal/metrics", nil)
	req2.Header.Set("Authorization", "Bearer k")
	rr2 := httptest.NewRecorder()
	a.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expect 200, got %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "cmdline") {
		t.Fatalf("expvar payload missing: %s", rr2.Body.String()[:200])
	}
}
