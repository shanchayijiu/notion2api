package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newIntegrationApp(t *testing.T) *App {
	t.Helper()
	return newAuditHTTPApp(t)
}

func TestIntegrationUpstreamTimeoutReturns504(t *testing.T) {
	app := newIntegrationApp(t)
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		return InferenceResult{}, context.DeadlineExceeded
	}
	body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status=%d want 504 body=%s", resp.Code, resp.Body.String())
	}
	assertOpenAIErrorCode(t, resp.Body.Bytes(), "upstream_timeout")
}

func TestIntegrationSessionExpiredReturns401(t *testing.T) {
	app := newIntegrationApp(t)
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		return InferenceResult{}, errors.New("upstream: session expired")
	}
	body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("session status=%d want 401 body=%s", resp.Code, resp.Body.String())
	}
	assertOpenAIErrorCode(t, resp.Body.Bytes(), "invalid_api_key")
}

func TestIntegrationStreamPartialThenEOFEmitsUpstreamAborted(t *testing.T) {
	oldFlushDelay := chatCompletionInitialFlushDelay
	chatCompletionInitialFlushDelay = 0
	t.Cleanup(func() { chatCompletionInitialFlushDelay = oldFlushDelay })

	app := newIntegrationApp(t)
	app.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		if err := sink.EmitText("partial answer"); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{}, errors.New("unexpected EOF")
	}
	body := `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", resp.Code, resp.Body.String())
	}
	bodyStr := resp.Body.String()
	if !strings.Contains(bodyStr, "upstream_aborted") {
		t.Fatalf("missing upstream_aborted error event:\n%s", bodyStr)
	}
	if strings.Contains(bodyStr, `"finish_reason":"stop"`) {
		t.Fatalf("aborted stream must not map stop:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Fatalf("missing DONE after abort:\n%s", bodyStr)
	}
}

func TestIntegrationPreHeaderStreamErrorUsesHTTPStatus(t *testing.T) {
	// Default flush delay (>0) so the proactive keepalive does not send headers
	// before the upstream fails.
	app := newIntegrationApp(t)
	app.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		return InferenceResult{}, &inferenceStepError{SubType: "quota-exhausted", Message: "quota"}
	}
	body := `{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("pre-header quota stream status=%d want 429 body=%s", resp.Code, resp.Body.String())
	}
	assertOpenAIErrorCode(t, resp.Body.Bytes(), "insufficient_quota")
}

func TestIntegrationClientDisconnectCancelsUpstream(t *testing.T) {
	oldFlushDelay := chatCompletionInitialFlushDelay
	chatCompletionInitialFlushDelay = 0
	t.Cleanup(func() { chatCompletionInitialFlushDelay = oldFlushDelay })

	app := newIntegrationApp(t)
	var canceled atomic.Bool
	app.runPromptStreamSinkOverride = func(r *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		select {
		case <-r.Context().Done():
			canceled.Store(true)
			return InferenceResult{}, r.Context().Err()
		case <-time.After(3 * time.Second):
			return InferenceResult{}, errors.New("timeout waiting for cancellation")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "http://x/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		app.ServeHTTP(rr, req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("request did not return after cancel")
	}
	if !canceled.Load() {
		t.Fatalf("upstream override did not observe context cancellation")
	}
}

func TestIntegrationErrorSchemaShape(t *testing.T) {
	app := newIntegrationApp(t)
	body := `{"model":"nope-model","messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("unknown model status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %s", resp.Body.String())
	}
	for _, key := range []string{"message", "type"} {
		if _, present := errObj[key]; !present {
			t.Fatalf("error missing %q: %s", key, resp.Body.String())
		}
	}
}

func assertOpenAIErrorCode(t *testing.T, body []byte, wantCode string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, string(body))
	}
	errObj := mapValue(payload["error"])
	if got := stringValue(errObj["code"]); got != wantCode {
		t.Fatalf("error code=%q want %q body=%s", got, wantCode, string(body))
	}
}
