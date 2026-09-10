package app

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSQLiteResponseRegressionConfig(t *testing.T, ttlSeconds int) AppConfig {
	t.Helper()
	persistResponses := true
	cfg := defaultConfig()
	cfg.APIKey = "test-key"
	cfg.Storage.SQLitePath = filepath.Join(t.TempDir(), "responses.sqlite")
	cfg.Storage.PersistConversations = false
	cfg.Storage.PersistResponses = &persistResponses
	cfg.Responses.StoreTTLSeconds = ttlSeconds
	cfg.Accounts = nil
	cfg.ActiveAccount = ""
	cfg.ProbeJSON = ""
	return normalizeConfig(cfg)
}

func TestResponsesSQLitePersistenceSurvivesRestartHTTP(t *testing.T) {
	cfg := newSQLiteResponseRegressionConfig(t, 60)
	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("create first server state: %v", err)
	}
	app := &App{
		State: state,
		runPromptOverride: func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
			return InferenceResult{Text: "persisted response", Prompt: request.Prompt}, nil
		},
	}

	first := auditAPIRequest(t, app, http.MethodPost, "/v1/responses", `{"model":"gpt-5.4","input":"persist this"}`)
	if first.Code != http.StatusOK {
		_ = state.Close()
		t.Fatalf("first response status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPayload map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		_ = state.Close()
		t.Fatalf("decode first response: %v", err)
	}
	responseID := stringValue(firstPayload["id"])
	if responseID == "" {
		_ = state.Close()
		t.Fatalf("first response has no id: %#v", firstPayload)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close first server state: %v", err)
	}

	restarted, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("restart server state: %v", err)
	}
	defer restarted.Close()
	restartedApp := &App{State: restarted}
	replay := auditAPIRequest(t, restartedApp, http.MethodGet, "/v1/responses/"+responseID, "")
	if replay.Code != http.StatusOK {
		t.Fatalf("replayed response status=%d body=%s", replay.Code, replay.Body.String())
	}
	if !strings.Contains(replay.Body.String(), responseID) || !strings.Contains(replay.Body.String(), "persisted response") {
		t.Fatalf("replayed response lost persisted payload: %s", replay.Body.String())
	}
}

func TestResponsesSQLiteTTLRemovesExpiredResponsesBeforeHTTPReplay(t *testing.T) {
	cfg := newSQLiteResponseRegressionConfig(t, 1)
	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("create server state: %v", err)
	}
	if state.Store == nil {
		_ = state.Close()
		t.Fatal("expected SQLite response store")
	}
	if err := state.Store.SaveResponse(
		"resp_expired",
		map[string]any{"id": "resp_expired", "output_text": "stale"},
		time.Now().UTC().Add(-2*time.Second),
		"",
		"",
		"",
	); err != nil {
		_ = state.Close()
		t.Fatalf("seed expired response: %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close seeded server state: %v", err)
	}

	restarted, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("restart server state: %v", err)
	}
	defer restarted.Close()
	replay := auditAPIRequest(t, &App{State: restarted}, http.MethodGet, "/v1/responses/resp_expired", "")
	if replay.Code != http.StatusNotFound {
		t.Fatalf("expired response status=%d body=%s", replay.Code, replay.Body.String())
	}
	if strings.Contains(replay.Body.String(), "stale") {
		t.Fatalf("expired response leaked through HTTP replay: %s", replay.Body.String())
	}
}
