package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDecodeRequestBodiesPreservesRawToolChoice(t *testing.T) {
	chatRaw := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"read_file"}},"x_future":true}`)
	chat, chatPayload, err := decodeChatCompletionsRequestBodyFromRaw(chatRaw)
	if err != nil {
		t.Fatalf("decode chat: %v", err)
	}
	if !reflect.DeepEqual(chat.ToolChoice, chatPayload["tool_choice"]) {
		t.Fatalf("typed chat tool_choice changed: typed=%#v raw=%#v", chat.ToolChoice, chatPayload["tool_choice"])
	}
	if _, ok := chatPayload["x_future"]; !ok {
		t.Fatal("raw chat payload dropped unknown fields")
	}

	responsesRaw := []byte(`{"model":"gpt-5.4","input":"hello","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"tool_choice":"none","instructions":"be concise"}`)
	responses, responsesPayload, err := decodeResponsesRequestBodyFromRaw(responsesRaw)
	if err != nil {
		t.Fatalf("decode responses: %v", err)
	}
	if responses.ToolChoice != responsesPayload["tool_choice"] {
		t.Fatalf("typed responses tool_choice changed: typed=%#v raw=%#v", responses.ToolChoice, responsesPayload["tool_choice"])
	}
	if responses.Instructions != responsesPayload["instructions"] {
		t.Fatalf("typed responses instructions changed: typed=%#v raw=%#v", responses.Instructions, responsesPayload["instructions"])
	}
	tools := parseToolList(responses.Tools)
	if len(tools) != 1 || stringValue(mapValue(tools[0]["function"])["name"]) != "read_file" {
		t.Fatalf("flat Responses function tool was not normalized: %#v", tools)
	}
}

func TestToolChoiceNoneIsHardBoundary(t *testing.T) {
	tools := []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}}}
	if toolChoiceNone("auto") {
		t.Fatal("auto must not be treated as none")
	}
	if !toolChoiceNone("none") || !toolChoiceNone(map[string]any{"type": "none"}) {
		t.Fatal("none forms were not recognized")
	}
	if toolsAllowedByChoice(tools, "none") {
		t.Fatal("tool_choice=none allowed tools")
	}
	result := InferenceResult{Text: `{"name":"read_file","arguments":{"path":"~/note.txt"}}`, ToolUses: []InferenceToolUse{{Name: "native", Arguments: `{"path":"~/native.txt"}`}}}
	payload := buildChatCompletionWithTools(result, "gpt-5.4", false, false)
	choice := mapValue(sliceValue(payload["choices"])[0])
	message := mapValue(choice["message"])
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("none boundary leaked native tool calls: %#v", message)
	}
	if len(extractToolCalls(result.Text)) == 0 {
		t.Fatal("fixture did not contain a structured tool call")
	}
}

func TestResponsesToolChoiceNoneDropsToolResultPrompt(t *testing.T) {
	input := []any{
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "secret tool result"},
	}
	got, err := normalizeResponsesInputFromPartsWithInstructionsAndToolResults(input, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("normalize none input: %v", err)
	}
	if strings.Contains(got.HiddenPrompt, "secret tool result") || strings.Contains(got.Prompt, "tool observation") {
		t.Fatalf("tool result crossed tool_choice=none boundary: %+v", got)
	}
	if got.Prompt != "" {
		t.Fatalf("none input should not synthesize a tool continuation prompt: %q", got.Prompt)
	}
	allowed, err := normalizeResponsesInputFromPartsWithInstructionsAndToolResults(input, nil, nil, nil, true)
	if err != nil || !strings.Contains(allowed.HiddenPrompt, "secret tool result") || allowed.Prompt == "" {
		t.Fatalf("tool result should remain available when tools are allowed: %+v err=%v", allowed, err)
	}
}

func TestResponsesToolChoiceNoneDoesNotEmitNativeToolCalls(t *testing.T) {
	state := &ServerState{Config: AppConfig{APIKey: "test-key"}, ModelRegistry: buildModelRegistry(AppConfig{})}
	app := &App{State: state, runPromptOverride: func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		if request.ToolsRaw != nil || request.MaskLocalPaths || request.ToolBridgeSection != "" {
			t.Fatalf("none request still carried tool bridge: %+v", request)
		}
		return InferenceResult{Text: `{"name":"read_file","arguments":{"path":"~/note.txt"}}`, ToolUses: []InferenceToolUse{{Name: "native", Arguments: `{"path":"~/native.txt"}`}}, Prompt: request.Prompt}, nil
	}}
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"say hello"},{"type":"function_call_output","call_id":"call_1","output":"secret"}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"tool_choice":"none"}`)
	req := httptest.NewRequest(http.MethodPost, "http://x/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("responses none status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, raw := range sliceValue(payload["output"]) {
		if stringValue(mapValue(raw)["type"]) == "function_call" {
			t.Fatalf("none response emitted function_call: %#v", payload["output"])
		}
	}
}

func TestSplitUTF8ChunksPreservesBytesAndRuneBoundaries(t *testing.T) {
	text := strings.Repeat("a", 127) + "中🙂é€𐍈" + strings.Repeat("b", 129)
	chunks := splitUTF8Chunks(text, 128)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks")
	}
	var joined strings.Builder
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i, chunk)
		}
		if len(chunk) > 128 {
			t.Fatalf("chunk %d exceeds byte limit: %d", i, len(chunk))
		}
		joined.WriteString(chunk)
	}
	if joined.String() != text || len(joined.String()) != len(text) {
		t.Fatalf("UTF-8 chunks changed bytes: got=%d want=%d", len(joined.String()), len(text))
	}
}

func TestStripCodePromptMarkersPreservesPythonIndentation(t *testing.T) {
	code := "  1 def main():\n    2     if True:\n    3         print(\"ok\")\n"
	got := stripCodePromptMarkers(code)
	want := "def main():\n    if True:\n        print(\"ok\")\n"
	if got != want {
		t.Fatalf("code indentation changed:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestClientWorkingDirectoryPathMaskRoundTrip(t *testing.T) {
	cwd := `C:\Users\Remote\project`
	inside := `C:\Users\Remote\project\src\main.go`
	outside := `D:\other\notes\draft.md`
	masked := maskLocalPathsForWorkingDirectory(`inside "`+inside+`" outside `+outside, cwd)
	if !strings.Contains(masked, "~/src/main.go") {
		t.Fatalf("workspace path was not masked relative to cwd: %q", masked)
	}
	if !strings.Contains(masked, "~/__client_path__/win/D/other/notes/draft.md") {
		t.Fatalf("outside path did not use reversible client marker: %q", masked)
	}
	args := `{"inside":"~/src/main.go","outside":"~/__client_path__/win/D/other/notes/draft.md"}`
	got := unmaskPathArgsForWorkingDirectory(args, cwd)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("unmask produced invalid JSON: %v", err)
	}
	if filepath.ToSlash(decoded["inside"]) != filepath.ToSlash(inside) || filepath.ToSlash(decoded["outside"]) != filepath.ToSlash(outside) {
		t.Fatalf("path round trip changed values: %#v", decoded)
	}
}

func newAuditHTTPApp(t *testing.T) *App {
	t.Helper()
	return &App{
		State: &ServerState{
			Config:        AppConfig{APIKey: "test-key"},
			ModelRegistry: buildModelRegistry(AppConfig{}),
		},
	}
}

func auditAPIRequest(t *testing.T, app *App, method string, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://x"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	return rr
}

func assertAuditToolBoundary(t *testing.T, request PromptRunRequest) {
	t.Helper()
	if request.ToolsRaw != nil || request.MaskLocalPaths || request.ToolBridgeSection != "" || request.AllowTextToolSynthesis {
		t.Fatalf("tool_choice=none crossed tool boundary: %+v", request)
	}
}

func TestAdminTestKeyRequiresAdminAuthAndReturnsConfiguredKey(t *testing.T) {
	app := &App{
		State: &ServerState{
			Config: AppConfig{
				APIKey: "wire-key",
				Admin: AdminConfig{
					Enabled:       true,
					Password:      "admin-password",
					TokenTTLHours: 1,
				},
			},
			AdminTokens: map[string]time.Time{},
		},
	}
	token := app.issueAdminToken()

	unauthorized := httptest.NewRecorder()
	app.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "http://x/admin/test/key", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized key request status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	if strings.Contains(unauthorized.Body.String(), "wire-key") {
		t.Fatalf("unauthorized response leaked api key: %s", unauthorized.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "http://x/admin/test/key", nil)
	req.Header.Set("X-Admin-Token", token)
	authorized := httptest.NewRecorder()
	app.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized key request status=%d body=%s", authorized.Code, authorized.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(authorized.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode authorized key response: %v", err)
	}
	if stringValue(payload["api_key"]) != "wire-key" {
		t.Fatalf("authorized key response=%#v", payload)
	}

	methodReq := httptest.NewRequest(http.MethodPost, "http://x/admin/test/key", nil)
	methodReq.Header.Set("X-Admin-Token", token)
	methodResp := httptest.NewRecorder()
	app.ServeHTTP(methodResp, methodReq)
	if methodResp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status=%d body=%s", methodResp.Code, methodResp.Body.String())
	}
}
func TestToolChoiceNoneHTTPBoundaryAcrossChatResponsesAndMessages(t *testing.T) {
	oldFlushDelay := chatCompletionInitialFlushDelay
	chatCompletionInitialFlushDelay = 0
	t.Cleanup(func() { chatCompletionInitialFlushDelay = oldFlushDelay })

	chatBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"say hello"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}],"tool_choice":"none"}`
	chat := newAuditHTTPApp(t)
	chat.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		assertAuditToolBoundary(t, request)
		return InferenceResult{Text: "hello", ToolUses: []InferenceToolUse{{Name: "read_file", Arguments: `{"path":"x"}`}}}, nil
	}
	chatResp := auditAPIRequest(t, chat, http.MethodPost, "/v1/chat/completions", chatBody)
	if chatResp.Code != http.StatusOK {
		t.Fatalf("chat none status=%d body=%s", chatResp.Code, chatResp.Body.String())
	}
	if strings.Contains(chatResp.Body.String(), `"tool_calls"`) {
		t.Fatalf("chat none emitted tool_calls: %s", chatResp.Body.String())
	}

	chatStream := newAuditHTTPApp(t)
	chatStream.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		assertAuditToolBoundary(t, request)
		if err := sink.EmitText("hello"); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{Text: "hello", ToolUses: []InferenceToolUse{{Name: "read_file", Arguments: `{"path":"x"}`}}}, nil
	}
	chatStreamResp := auditAPIRequest(t, chatStream, http.MethodPost, "/v1/chat/completions", strings.Replace(chatBody, `"tool_choice":"none"`, `"tool_choice":"none","stream":true`, 1))
	if chatStreamResp.Code != http.StatusOK || !strings.Contains(chatStreamResp.Body.String(), "data: [DONE]") {
		t.Fatalf("chat none stream status=%d body=%s", chatStreamResp.Code, chatStreamResp.Body.String())
	}
	if strings.Contains(chatStreamResp.Body.String(), `"tool_calls"`) {
		t.Fatalf("chat none stream emitted tool_calls: %s", chatStreamResp.Body.String())
	}

	responsesBody := `{"model":"gpt-5.4","input":"say hello","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"tool_choice":"none"}`
	responses := newAuditHTTPApp(t)
	responses.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		assertAuditToolBoundary(t, request)
		return InferenceResult{Text: "hello", ToolUses: []InferenceToolUse{{Name: "read_file", Arguments: `{"path":"x"}`}}}, nil
	}
	responsesResp := auditAPIRequest(t, responses, http.MethodPost, "/v1/responses", responsesBody)
	if responsesResp.Code != http.StatusOK {
		t.Fatalf("responses none status=%d body=%s", responsesResp.Code, responsesResp.Body.String())
	}
	if strings.Contains(responsesResp.Body.String(), `"type":"function_call"`) {
		t.Fatalf("responses none emitted function_call: %s", responsesResp.Body.String())
	}

	responsesStream := newAuditHTTPApp(t)
	responsesStream.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		assertAuditToolBoundary(t, request)
		if err := sink.EmitText("hello"); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{Text: "hello", ToolUses: []InferenceToolUse{{Name: "read_file", Arguments: `{"path":"x"}`}}}, nil
	}
	responsesStreamResp := auditAPIRequest(t, responsesStream, http.MethodPost, "/v1/responses", strings.Replace(responsesBody, `"tool_choice":"none"`, `"tool_choice":"none","stream":true`, 1))
	if responsesStreamResp.Code != http.StatusOK || !strings.Contains(responsesStreamResp.Body.String(), "data: [DONE]") {
		t.Fatalf("responses none stream status=%d body=%s", responsesStreamResp.Code, responsesStreamResp.Body.String())
	}
	if strings.Contains(responsesStreamResp.Body.String(), `"type":"function_call"`) {
		t.Fatalf("responses none stream emitted function_call: %s", responsesStreamResp.Body.String())
	}

	messagesBody := `{"model":"gpt-5.4","messages":[{"role":"user","content":"say hello"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}],"tool_choice":"none","max_tokens":64}`
	messages := newAuditHTTPApp(t)
	messages.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		assertAuditToolBoundary(t, request)
		return InferenceResult{Text: "hello", ToolUses: []InferenceToolUse{{Name: "read_file", Arguments: `{"path":"x"}`}}}, nil
	}
	messagesResp := auditAPIRequest(t, messages, http.MethodPost, "/v1/messages", messagesBody)
	if messagesResp.Code != http.StatusOK {
		t.Fatalf("messages none status=%d body=%s", messagesResp.Code, messagesResp.Body.String())
	}
	if strings.Contains(messagesResp.Body.String(), `"type":"tool_use"`) {
		t.Fatalf("messages none emitted tool_use: %s", messagesResp.Body.String())
	}

	messagesStream := newAuditHTTPApp(t)
	messagesStream.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		assertAuditToolBoundary(t, request)
		if err := sink.EmitText("hello"); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{Text: "hello", ToolUses: []InferenceToolUse{{Name: "read_file", Arguments: `{"path":"x"}`}}}, nil
	}
	messagesStreamResp := auditAPIRequest(t, messagesStream, http.MethodPost, "/v1/messages", strings.Replace(messagesBody, `"tool_choice":"none"`, `"tool_choice":"none","stream":true`, 1))
	if messagesStreamResp.Code != http.StatusOK || !strings.Contains(messagesStreamResp.Body.String(), "message_stop") {
		t.Fatalf("messages none stream status=%d body=%s", messagesStreamResp.Code, messagesStreamResp.Body.String())
	}
	if strings.Contains(messagesStreamResp.Body.String(), `"type":"tool_use"`) {
		t.Fatalf("messages none stream emitted tool_use: %s", messagesStreamResp.Body.String())
	}
}

func TestResponsesHTTPMultiTurnFunctionCallOutputAndReplay(t *testing.T) {
	app := newAuditHTTPApp(t)
	calls := 0
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		calls++
		switch calls {
		case 1:
			if len(request.ToolsRaw) != 1 || !request.MaskLocalPaths {
				t.Fatalf("first Responses turn lost tools: %+v", request)
			}
			return InferenceResult{
				Text:   `{"name":"read_file","arguments":{"path":"~/note.txt"}}`,
				Prompt: request.Prompt,
			}, nil
		case 2:
			if !strings.Contains(request.HiddenPrompt, "secret result") {
				t.Fatalf("second Responses turn did not receive function_call_output: %+v", request)
			}
			return InferenceResult{Text: "The file says hello.", Prompt: request.Prompt}, nil
		default:
			t.Fatalf("unexpected inference call %d", calls)
			return InferenceResult{}, nil
		}
	}

	firstBody := `{"model":"gpt-5.4","input":"read note.txt","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"tool_choice":"required"}`
	first := auditAPIRequest(t, app, http.MethodPost, "/v1/responses", firstBody)
	if first.Code != http.StatusOK {
		t.Fatalf("first Responses turn status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPayload map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatalf("decode first Responses response: %v", err)
	}
	responseID := stringValue(firstPayload["id"])
	if responseID == "" {
		t.Fatalf("first Responses response has no id: %#v", firstPayload)
	}
	firstOutput := sliceValue(firstPayload["output"])
	if len(firstOutput) != 2 || stringValue(mapValue(firstOutput[1])["type"]) != "function_call" {
		t.Fatalf("first Responses output missing function_call: %#v", firstOutput)
	}
	callID := stringValue(mapValue(firstOutput[1])["call_id"])
	if callID == "" {
		t.Fatalf("first function_call has no call_id: %#v", firstOutput[1])
	}

	getReq := httptest.NewRequest(http.MethodGet, "http://x/v1/responses/"+responseID, nil)
	getReq.Header.Set("Authorization", "Bearer test-key")
	getResp := httptest.NewRecorder()
	app.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK || !strings.Contains(getResp.Body.String(), responseID) {
		t.Fatalf("stored Responses replay status=%d body=%s", getResp.Code, getResp.Body.String())
	}

	secondBody := `{"model":"gpt-5.4","previous_response_id":"` + responseID + `","input":[{"type":"function_call_output","call_id":"` + callID + `","output":"secret result"}]}`
	second := auditAPIRequest(t, app, http.MethodPost, "/v1/responses", secondBody)
	if second.Code != http.StatusOK {
		t.Fatalf("second Responses turn status=%d body=%s", second.Code, second.Body.String())
	}
	var secondPayload map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &secondPayload); err != nil {
		t.Fatalf("decode second Responses response: %v", err)
	}
	if stringValue(secondPayload["output_text"]) != "The file says hello." {
		t.Fatalf("second Responses output_text=%#v body=%s", secondPayload["output_text"], second.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected two inference calls, got %d", calls)
	}
}
func TestResponsesCompletedOutputMatchesTerminalItemsForFunctionCalls(t *testing.T) {
	result := InferenceResult{Text: "", Reasoning: ""}
	calls := []OpenAIToolCall{
		{ID: "call_1", OutputItemID: "fc_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"x"}`}},
	}
	payload := buildResponsesOutputWithCalls(result, "gpt-5.4", false, "resp_1", "msg_1", 1, calls)
	output := sliceValue(payload["output"])
	if len(output) != 2 {
		t.Fatalf("function-call response must retain terminal message and call items: %#v", output)
	}
	if stringValue(mapValue(output[0])["id"]) != "msg_1" || stringValue(mapValue(output[1])["id"]) != "fc_1" {
		t.Fatalf("completed output IDs do not match stream terminal items: %#v", output)
	}
}

func TestResponsesLiveStreamFunctionCallEventsReconcileWithCompletedOutput(t *testing.T) {
	oldFlushDelay := chatCompletionInitialFlushDelay
	chatCompletionInitialFlushDelay = 0
	t.Cleanup(func() { chatCompletionInitialFlushDelay = oldFlushDelay })

	app := newAuditHTTPApp(t)
	app.runPromptStreamSinkOverride = func(_ *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		if request.ToolsRaw == nil || !request.MaskLocalPaths {
			t.Fatalf("stream fixture did not receive tools: %+v", request)
		}
		if err := sink.EmitReasoning("思考"); err != nil {
			return InferenceResult{}, err
		}
		if err := sink.EmitText(""); err != nil {
			return InferenceResult{}, err
		}
		return InferenceResult{
			Text:      `{"name":"read_file","arguments":{"path":"~/中文/🙂.txt"}} {"name":"write_file","arguments":{"path":"~/out.txt","content":"ok"}}`,
			Reasoning: "思考",
		}, nil
	}
	body := `{"model":"gpt-5.4","input":"use tools","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}},{"type":"function","name":"write_file","parameters":{"type":"object"}}],"tool_choice":"required","stream":true}`
	resp := auditAPIRequest(t, app, http.MethodPost, "/v1/responses", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("responses stream status=%d body=%s", resp.Code, resp.Body.String())
	}

	type sseEvent struct {
		Name string
		Data string
	}
	var events []sseEvent
	var current sseEvent
	flush := func() {
		if current.Name != "" || current.Data != "" {
			events = append(events, current)
		}
		current = sseEvent{}
	}
	for _, line := range strings.Split(strings.ReplaceAll(resp.Body.String(), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			current.Name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.Data = strings.TrimPrefix(line, "data: ")
		case line == "":
			flush()
		}
	}
	flush()
	if len(events) == 0 {
		t.Fatalf("no SSE events: %s", resp.Body.String())
	}

	sequence := make([]int, 0, len(events))
	responseIDs := map[string]bool{}
	terminalItems := map[string]map[string]any{}
	argumentParts := map[string]string{}
	outputIndices := map[string]int{}
	var completed map[string]any
	for _, event := range events {
		if event.Data == "[DONE]" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			t.Fatalf("decode %s: %v (%q)", event.Name, err, event.Data)
		}
		seq, ok := payload["sequence_number"].(float64)
		if !ok {
			t.Fatalf("%s has no sequence_number: %#v", event.Name, payload)
		}
		sequence = append(sequence, int(seq))
		if id := strings.TrimSpace(stringValue(payload["response_id"])); id != "" {
			responseIDs[id] = true
		}
		switch event.Name {
		case "response.output_item.added", "response.output_item.done":
			index := intValue(payload["output_index"])
			item := mapValue(payload["item"])
			if item == nil {
				t.Fatalf("%s missing item: %#v", event.Name, payload)
			}
			id := stringValue(item["id"])
			if id == "" {
				t.Fatalf("%s missing item id: %#v", event.Name, payload)
			}
			outputIndices[id] = index
			if event.Name == "response.output_item.done" {
				terminalItems[id] = item
			}
		case "response.function_call_arguments.delta":
			id := stringValue(payload["item_id"])
			argumentParts[id] += stringValue(payload["delta"])
		case "response.completed":
			completed = mapValue(payload["response"])
		}
	}
	if len(sequence) < 2 {
		t.Fatalf("too few events: %#v", events)
	}
	for i, got := range sequence {
		if got != i {
			t.Fatalf("sequence_number[%d]=%d, want %d", i, got, i)
		}
	}
	if len(responseIDs) != 1 {
		t.Fatalf("inconsistent response IDs: %#v", responseIDs)
	}
	if completed == nil {
		t.Fatalf("missing response.completed event")
	}
	completedOutput := sliceValue(completed["output"])
	completedByID := map[string]map[string]any{}
	for _, raw := range completedOutput {
		item := mapValue(raw)
		if item != nil {
			completedByID[stringValue(item["id"])] = item
		}
	}
	if len(completedByID) != 3 {
		t.Fatalf("completed output should contain message plus two calls: %#v", completedOutput)
	}
	if outputIndices[stringValue(mapValue(completedOutput[0])["id"])] != 0 {
		t.Fatalf("message item is not output_index 0: %#v", outputIndices)
	}
	callCount := 0
	for id, item := range terminalItems {
		if stringValue(item["type"]) != "function_call" {
			continue
		}
		callCount++
		if outputIndices[id] < 1 {
			t.Fatalf("function call has invalid output index: id=%s indices=%#v", id, outputIndices)
		}
		completedItem, ok := completedByID[id]
		if !ok {
			t.Fatalf("terminal item %s missing from completed output: %#v", id, completedOutput)
		}
		if stringValue(completedItem["call_id"]) != stringValue(item["call_id"]) {
			t.Fatalf("call_id changed for %s: terminal=%#v completed=%#v", id, item, completedItem)
		}
		if argumentParts[id] != stringValue(item["arguments"]) {
			t.Fatalf("argument deltas changed for %s: got=%q want=%q", id, argumentParts[id], stringValue(item["arguments"]))
		}
	}
	if callCount != 2 {
		t.Fatalf("expected two terminal function calls, got %d: %#v", callCount, terminalItems)
	}
	if !strings.HasSuffix(strings.TrimSpace(resp.Body.String()), "data: [DONE]") {
		t.Fatalf("[DONE] must terminate the SSE stream: %s", resp.Body.String())
	}
}
