package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestClassifyUpstreamError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"quota step", &inferenceStepError{SubType: "quota-exhausted", Message: "no quota"}, http.StatusTooManyRequests, "insufficient_quota"},
		{"premium step", &inferenceStepError{SubType: "premium-feature-unavailable"}, http.StatusTooManyRequests, "insufficient_quota"},
		{"http 402", &notionAPIError{StatusCode: http.StatusPaymentRequired, Message: "pay"}, http.StatusTooManyRequests, "insufficient_quota"},
		{"http 429", &notionAPIError{StatusCode: http.StatusTooManyRequests, Message: "slow down"}, http.StatusTooManyRequests, "rate_limit_exceeded"},
		{"http 401", &notionAPIError{StatusCode: http.StatusUnauthorized, Message: "nope"}, http.StatusUnauthorized, "invalid_api_key"},
		{"http 503", &notionAPIError{StatusCode: http.StatusServiceUnavailable, Message: "down"}, http.StatusServiceUnavailable, "upstream_unavailable"},
		{"starved", errAccountStarved, http.StatusServiceUnavailable, "upstream_unavailable"},
		{"timeout", errors.New("context deadline exceeded"), http.StatusGatewayTimeout, "upstream_timeout"},
		{"bare quota message", errors.New("upstream: premium-feature-unavailable"), http.StatusTooManyRequests, "insufficient_quota"},
		{"unknown", errors.New("weird failure"), http.StatusBadGateway, "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class := classifyUpstreamError(tc.err)
			if class.Status != tc.wantStatus || class.Code != tc.wantCode {
				t.Fatalf("classify(%v) = %d/%s want %d/%s", tc.err, class.Status, class.Code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestQuotaErrorHTTPStatusAndCode(t *testing.T) {
	app := newAuditHTTPApp(t)
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		return InferenceResult{}, &inferenceStepError{SubType: "quota-exhausted", Message: "workspace quota exhausted"}
	}
	body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status=%d want 429 body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj := mapValue(payload["error"])
	if got := stringValue(errObj["code"]); got != "insufficient_quota" {
		t.Fatalf("quota code=%q want insufficient_quota body=%s", got, resp.Body.String())
	}
}
