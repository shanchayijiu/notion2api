package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestIDEchoed(t *testing.T) {
	app := newAuditHTTPApp(t)

	req := httptest.NewRequest(http.MethodGet, "http://x/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Request-Id", "trace-me-123")
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	if got := rr.Header().Get("X-Request-Id"); got != "trace-me-123" {
		t.Fatalf("X-Request-Id=%q want trace-me-123", got)
	}

	fresh := httptest.NewRecorder()
	app.ServeHTTP(fresh, httptest.NewRequest(http.MethodGet, "http://x/healthz", nil))
	if got := fresh.Header().Get("X-Request-Id"); !strings.HasPrefix(got, "req_") {
		t.Fatalf("generated X-Request-Id=%q", got)
	}
}

func TestRequestTimerMarksFirstTokenOnce(t *testing.T) {
	timer := newRequestTimer("req_test")
	if timer.TTFT() != 0 {
		t.Fatalf("TTFT should be zero before first token")
	}
	if timer.HasToken() {
		t.Fatalf("HasToken should be false before first token")
	}
	time.Sleep(20 * time.Millisecond)
	timer.MarkToken()
	first := timer.TTFT()
	if first <= 0 {
		t.Fatalf("TTFT not recorded: %v", first)
	}
	timer.MarkToken()
	if timer.TTFT() != first {
		t.Fatalf("TTFT changed on later tokens: %v -> %v", first, timer.TTFT())
	}
	if snap := timer.snapshot(0); snap.TTFT != first {
		t.Fatalf("snapshot TTFT=%v want %v", snap.TTFT, first)
	}
}

func TestResolveInitialFlushDelaySwitch(t *testing.T) {
	old := chatCompletionInitialFlushDelay
	t.Cleanup(func() { chatCompletionInitialFlushDelay = old })
	chatCompletionInitialFlushDelay = 1500 * time.Millisecond

	if got := resolveInitialFlushDelay(AppConfig{}); got != 1500*time.Millisecond {
		t.Fatalf("default flush delay=%v want 1.5s", got)
	}
	zero := 0
	if got := resolveInitialFlushDelay(AppConfig{Streaming: StreamingConfig{InitialFlushDelayMS: &zero}}); got != 0 {
		t.Fatalf("zero flush delay=%v want 0", got)
	}
	ms := 250
	if got := resolveInitialFlushDelay(AppConfig{Streaming: StreamingConfig{InitialFlushDelayMS: &ms}}); got != 250*time.Millisecond {
		t.Fatalf("configured flush delay=%v want 250ms", got)
	}
}

func TestStreamingRecordsTTFTMetric(t *testing.T) {
	resetMetricsForTest()
	oldFlushDelay := chatCompletionInitialFlushDelay
	chatCompletionInitialFlushDelay = 0
	t.Cleanup(func() {
		chatCompletionInitialFlushDelay = oldFlushDelay
		resetMetricsForTest()
	})

	app := newAuditHTTPApp(t)
	app.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		if err := sink.EmitText("hello"); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{Text: "hello", Prompt: request.Prompt}, nil
	}
	body := `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}

	ttftMu.Lock()
	count := ttftSeries.count
	ttftMu.Unlock()
	if count == 0 {
		t.Fatalf("TTFT was not observed")
	}
}
