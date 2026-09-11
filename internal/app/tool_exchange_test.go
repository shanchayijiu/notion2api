package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBuildToolExchangePromptPairsCallsWithResults(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "read note.txt"},
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{
				"id":   "call_1",
				"type": "function",
				"function": map[string]any{
					"name":      "read_file",
					"arguments": `{"path":"~/note.txt"}`,
				},
			},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "hello world"},
	}
	got := buildToolExchangePrompt(messages)
	for _, want := range []string{"read_file", `{"path":"~/note.txt"}`, "call_1", "hello world"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool exchange missing %q:\n%s", want, got)
		}
	}
}

func TestBuildToolExchangePromptHandlesResponsesItems(t *testing.T) {
	messages := []any{
		map[string]any{"type": "function_call", "call_id": "c9", "name": "write_file", "arguments": `{"path":"a.txt","content":"hi"}`},
		map[string]any{"type": "function_call_output", "call_id": "c9", "output": "written"},
	}
	got := buildToolExchangePrompt(messages)
	for _, want := range []string{"write_file", "c9", "written"} {
		if !strings.Contains(got, want) {
			t.Fatalf("responses tool exchange missing %q:\n%s", want, got)
		}
	}
}

func TestChatToolCallContinuationFeedsBackCallContext(t *testing.T) {
	app := newAuditHTTPApp(t)
	toolsRaw := `[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]`

	calls := 0
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		calls++
		switch calls {
		case 1:
			if !request.MaskLocalPaths || len(request.ToolsRaw) != 1 {
				t.Fatalf("first turn lost tools: %+v", request)
			}
			return InferenceResult{
				Prompt: request.Prompt,
				Text:   `{"name":"read_file","arguments":{"path":"~/note.txt"}}`,
			}, nil
		case 2:
			section := request.ToolBridgeSection
			for _, want := range []string{"read_file", "call_1", "hello world"} {
				if !strings.Contains(section, want) {
					t.Fatalf("second turn tool bridge missing %q:\n%s", want, section)
				}
			}
			return InferenceResult{Prompt: request.Prompt, Text: "The file says hello world."}, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return InferenceResult{}, nil
		}
	}

	firstBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"read note.txt"}],"tools":` + toolsRaw + `}`
	first := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", firstBody)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPayload map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	message := mapValue(sliceValue(firstPayload["choices"])[0])["message"]
	toolCalls := sliceValue(mapValue(message)["tool_calls"])
	if len(toolCalls) != 1 {
		t.Fatalf("first turn produced %d tool calls: %s", len(toolCalls), first.Body.String())
	}
	call := mapValue(toolCalls[0])
	if funcName := stringValue(mapValue(call["function"])["name"]); funcName != "read_file" {
		t.Fatalf("unexpected tool name %q", funcName)
	}

	secondBody := `{"model":"gpt-5.4","messages":[
		{"role":"user","content":"read note.txt"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"~/note.txt\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"hello world"}
	],"tools":` + toolsRaw + `}`
	second := auditAPIRequest(t, app, http.MethodPost, "/v1/chat/completions", secondBody)
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
}
