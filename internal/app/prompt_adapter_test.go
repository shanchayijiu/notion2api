package app

import (
	"net/http"
	"strings"
	"testing"
)

func newPromptAdapterApp(profile string, prefix string) *App {
	return &App{
		State: &ServerState{
			Config: AppConfig{
				APIKey: "test-key",
				Prompt: PromptConfig{
					Profile:                  profile,
					CognitiveReframingPrefix: prefix,
				},
			},
			ModelRegistry: buildModelRegistry(AppConfig{}),
		},
	}
}

func TestPromptAdapterInjectsConfiguredFraming(t *testing.T) {
	app := newPromptAdapterApp("cognitive_reframing", "REFRAME-MARKER")
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		if request.PromptAdapterName != "cognitive_reframing" {
			t.Fatalf("adapter name=%q", request.PromptAdapterName)
		}
		if !strings.Contains(request.HiddenPrompt, "REFRAME-MARKER") {
			t.Fatalf("reframing prefix not injected: %q", request.HiddenPrompt)
		}
		return InferenceResult{Text: "ok", Prompt: request.Prompt}, nil
	}
	body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPromptAdapterNativeWhenDisabled(t *testing.T) {
	app := newPromptAdapterApp("none", "SHOULD-NOT-APPEAR")
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		if strings.Contains(request.HiddenPrompt, "SHOULD-NOT-APPEAR") {
			t.Fatalf("disabled adapter still injected: %q", request.HiddenPrompt)
		}
		if request.PromptAdapterName != "" {
			t.Fatalf("disabled adapter name=%q", request.PromptAdapterName)
		}
		return InferenceResult{Text: "ok", Prompt: request.Prompt}, nil
	}
	body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPromptAdapterIsIdempotent(t *testing.T) {
	app := newPromptAdapterApp("cognitive_reframing", "ONCE")
	request := PromptRunRequest{Prompt: "hi"}
	once := app.adaptPromptRequest(request)
	twice := app.adaptPromptRequest(once)
	if strings.Count(twice.HiddenPrompt, "ONCE") != 1 {
		t.Fatalf("adapter applied more than once: %q", twice.HiddenPrompt)
	}
}
