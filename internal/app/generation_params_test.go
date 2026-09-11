package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestChatGenerationParamsMaxTokensTruncatesAndSetsLength(t *testing.T) {
	app := newAuditHTTPApp(t)
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		if request.MaxOutputTokens != 2 {
			t.Fatalf("max_tokens not propagated: %d", request.MaxOutputTokens)
		}
		return InferenceResult{Text: strings.Repeat("word ", 40), Prompt: request.Prompt}, nil
	}
	body := `{"model":"gpt-5.4","max_tokens":2,"messages":[{"role":"user","content":"write"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	choices := sliceValue(payload["choices"])
	choice := mapValue(choices[0])
	if got := stringValue(choice["finish_reason"]); got != "length" {
		t.Fatalf("finish_reason=%q want length", got)
	}
	content := stringValue(mapValue(choice["message"])["content"])
	if estimateTokens(content) > 2 {
		t.Fatalf("content not truncated to max_tokens: %q", content)
	}
}

func TestChatGenerationParamsRejectsMultipleChoices(t *testing.T) {
	app := newAuditHTTPApp(t)
	body := `{"model":"gpt-5.4","n":2,"messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("n=2 status=%d want 400 body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "n=2") {
		t.Fatalf("n=2 error not explicit: %s", resp.Body.String())
	}
}

func TestChatGenerationParamsRejectsOutOfRangeTemperature(t *testing.T) {
	app := newAuditHTTPApp(t)
	body := `{"model":"gpt-5.4","temperature":3.5,"messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("temperature=3.5 status=%d want 400 body=%s", resp.Code, resp.Body.String())
	}
}

func TestChatGenerationParamsUnsupportedSurfacedNotSilent(t *testing.T) {
	app := newAuditHTTPApp(t)
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		return InferenceResult{Text: "ok", Prompt: request.Prompt}, nil
	}
	body := `{"model":"gpt-5.4","temperature":0.5,"presence_penalty":0.2,"messages":[{"role":"user","content":"hi"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	header := resp.Header().Get("X-Notion2API-Unsupported-Params")
	if !strings.Contains(header, "temperature") || !strings.Contains(header, "presence_penalty") {
		t.Fatalf("unsupported params not surfaced: %q", header)
	}
}

func TestChatGenerationParamsResponseFormatInstructionInjected(t *testing.T) {
	app := newAuditHTTPApp(t)
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		if !strings.Contains(request.HiddenPrompt, "JSON object") {
			t.Fatalf("json_object instruction missing from hidden prompt: %q", request.HiddenPrompt)
		}
		return InferenceResult{Text: `{"ok":true}`, Prompt: request.Prompt}, nil
	}
	body := `{"model":"gpt-5.4","response_format":{"type":"json_object"},"messages":[{"role":"user","content":"give json"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestChatGenerationParamsParallelToolCallsFalseKeepsFirst(t *testing.T) {
	app := newAuditHTTPApp(t)
	toolsRaw := `[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}},{"type":"function","function":{"name":"write_file","parameters":{"type":"object"}}}]`
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		return InferenceResult{
			Prompt: request.Prompt,
			Text:   `{"name":"read_file","arguments":{"path":"a"}} {"name":"write_file","arguments":{"path":"b","content":"c"}}`,
		}, nil
	}
	body := `{"model":"gpt-5.4","messages":[{"role":"user","content":"do"}],"tools":` + toolsRaw + `,"parallel_tool_calls":false}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	message := mapValue(sliceValue(payload["choices"])[0])["message"]
	calls := sliceValue(mapValue(message)["tool_calls"])
	if len(calls) != 1 {
		t.Fatalf("parallel_tool_calls=false kept %d calls: %s", len(calls), resp.Body.String())
	}
}

func TestChatGenerationParamsStreamMaxTokensSetsLength(t *testing.T) {
	oldFlushDelay := chatCompletionInitialFlushDelay
	chatCompletionInitialFlushDelay = 0
	t.Cleanup(func() { chatCompletionInitialFlushDelay = oldFlushDelay })

	app := newAuditHTTPApp(t)
	app.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		long := strings.Repeat("tok ", 50)
		if err := sink.EmitText(long); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{Text: long, Prompt: request.Prompt}, nil
	}
	body := `{"model":"gpt-5.4","stream":true,"max_tokens":3,"messages":[{"role":"user","content":"write"}]}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"finish_reason":"length"`) {
		t.Fatalf("stream max_tokens did not set length: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "data: [DONE]") {
		t.Fatalf("stream missing DONE: %s", resp.Body.String())
	}
}
