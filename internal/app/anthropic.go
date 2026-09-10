package app

// anthropic.go — POST /v1/messages 兼容端点（2026-08-26，opus 设计 F）
// Claude Code 原生 Anthropic 格式工具传递路径。请求→内部 OpenAI 形态→现有链路
//（认知重构/合成/轮换）→ 响应转回 Anthropic 格式（非流式 JSON / 流式 SSE 事件）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// decodeAnthropicMessageRequestBodyFromRaw retains the raw map so adapter-specific
// fields are not discarded even though execution currently uses the typed view.
func decodeAnthropicMessageRequestBodyFromRaw(raw []byte) (anthropicMessageRequest, map[string]any, error) {
	typed, err := decodeTypedBodyFromRaw[anthropicMessageRequest](raw)
	if err != nil {
		return anthropicMessageRequest{}, nil, err
	}
	payload, err := decodeBodyMapFromRaw(raw)
	if err != nil {
		return anthropicMessageRequest{}, nil, err
	}
	typed.rawPayload = payload
	return typed, payload, nil
}

// handleMessages — POST /v1/messages
func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := a.decodeBodyRaw(w, r)
	if err != nil {
		if errors.Is(err, errRequestTooLarge) {
			writeAnthropicError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds configured limit")
		} else {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		}
		return
	}
	req, payload, err := decodeAnthropicMessageRequestBodyFromRaw(raw)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	_ = payload // retained for adapter-level compatibility and unknown-field tests
	if strings.TrimSpace(req.Model) == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if len(req.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "messages must be a non-empty array")
		return
	}
	if req.MaxTokens == nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "max_tokens is required")
		return
	}
	// 转换请求到内部 OpenAI 形态（C3：屏蔽子代理类工具，CC 本地直接执行常规工具）
	toolChoice, _, err := anthropicToolChoiceToOpenAI(req.ToolChoice)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	disableParallelToolUse := req.DisableParallelToolUse || anthropicNestedDisableParallelToolUse(req.ToolChoice)
	toolsAllowed := !toolChoiceNone(toolChoice)
	internalTools := filterSubagentTools(anthropicToolsToOpenAI(req.Tools))
	if !toolsAllowed {
		internalTools = nil
	}
	internalMessages := parseAnthropicMessages(req.Messages)
	if system := parseAnthropicSystem(req.System); system != "" {
		// The internal Chat seam only has a messages transcript. Keep system exactly
		// once here; do not also place it in an ignored top-level Chat field.
		internalMessages = append([]map[string]any{{"role": "system", "content": system}}, internalMessages...)
	}
	if len(internalMessages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "messages must contain text content")
		return
	}
	internalBody := map[string]any{
		"model":      req.Model,
		"messages":   internalMessages,
		"stream":     req.Stream,
		"max_tokens": *req.MaxTokens,
	}
	if req.Temperature != nil {
		internalBody["temperature"] = *req.Temperature
	}
	if req.Metadata != nil {
		internalBody["metadata"] = req.Metadata
	}
	if disableParallelToolUse {
		internalBody["disable_parallel_tool_use"] = true
	}
	if len(internalTools) > 0 {
		internalBody["tools"] = internalTools
		if toolChoice != "" && toolChoice != "auto" {
			internalBody["tool_choice"] = toolChoice
		}
	}
	if len(req.StopSequences) > 0 {
		internalBody["stop"] = req.StopSequences
	}
	if req.UseWebSearch != nil {
		internalBody["use_web_search"] = *req.UseWebSearch
	}
	// 携带 anthropic 标记（内部请求构建可感知来源，暂不特殊处理）
	if req.Stream {
		a.handleMessagesStream(w, r, internalBody, req)
		return
	}
	a.handleMessagesNonStream(w, r, internalBody, req)
}

// handleMessagesNonStream — 非流式：复用 chat/completions 非流式链路后转 Anthropic 格式
func (a *App) handleMessagesNonStream(w http.ResponseWriter, r *http.Request, internalBody map[string]any, req anthropicMessageRequest) {
	rec := &responseRecorder{header: http.Header{}}
	rec.header.Set("Content-Type", "application/json")
	// 复用 chat completions 处理（它内部完成 normalize/注入/推理/工具合成）
	a.handleChatCompletions(rec, rWithBody(r, internalBody))
	if rec.status >= 400 {
		var payload map[string]any
		if json.Unmarshal([]byte(rec.body.String()), &payload) == nil {
			writeAnthropicErrorFromOpenAI(w, rec.status, payload)
			return
		}
		writeAnthropicError(w, rec.status, "api_error", rec.body.String())
		return
	}
	var oai map[string]any
	if err := json.Unmarshal([]byte(rec.body.String()), &oai); err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "internal response decode failed")
		return
	}
	// OpenAI → Anthropic 响应转换
	resp := anthropicFromOpenAICompletion(oai)
	writeJSON(w, http.StatusOK, resp)
}

// handleMessagesStream — 流式：复用 chat 流式链路，输出经 OpenAI→Anthropic 事件转换器。
// 2026-08-26 修复伪流式（此前 responseRecorder 全缓冲 → 生成完才输出 = 用户看不到流式）：
// liveSSEWriter 把内部逐块 SSE 实时转发到管道，转换器逐行实时转 Anthropic 事件。
func (a *App) handleMessagesStream(w http.ResponseWriter, r *http.Request, internalBody map[string]any, req anthropicMessageRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	lw := &liveSSEWriter{pw: pw, header: http.Header{}}
	go func() {
		defer close(done)
		// P0-3 修复：子 goroutine panic 必须由本 goroutine recover（ServeHTTP 顶层
		// recover 覆盖不到这里）；无论正常/异常结束都必须关闭写端，
		// 否则 converter 的 scanner 永久阻塞在 pipe 读端（goroutine 泄漏 + 死锁）。
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[messages] stream producer panic: %v\n%s", rec, debug.Stack())
			}
			_ = pw.Close()
		}()
		a.handleChatCompletions(lw, rWithBody(r, internalBody))
	}()

	msgID := "msg_" + strings.ReplaceAll(randomUUID(), "-", "")
	converter := newAnthropicEventConverter(w, flusher, msgID, req.Model)
	converter.upstream = lw // P1-1：让转换器能看到内部链路真实状态码，错误不再吞成空成功
	runErr := converter.run(pr)
	// P0-3 修复：converter 提前返回（如超长行 scan 失败）时关闭读端，
	// 使写端 pw.Write 立即返回 ErrClosedPipe，避免 io.Pipe 永久互锁。
	_ = pr.Close()
	if runErr != nil {
		log.Printf("[messages] stream convert error: %v", runErr)
	}
	// 客户端断连时不空等 producer 排空（上游 ctx 已随请求取消）
	select {
	case <-done:
	case <-r.Context().Done():
	}
}

// liveSSEWriter — 内部流式 http.ResponseWriter 适配：Write 实时转发到管道（无缓冲）
// 记录真实 status/header：内部链路在 SSE headers 发出前失败时写的是 JSON 错误体，
// 转换器据此把错误透传为 Anthropic error 事件，而不是空成功消息（P1-1）。
type liveSSEWriter struct {
	pw     *io.PipeWriter
	header http.Header
	mu     sync.RWMutex
	status int
}

func (lw *liveSSEWriter) Header() http.Header { return lw.header }
func (lw *liveSSEWriter) Write(b []byte) (int, error) {
	lw.mu.Lock()
	if lw.status == 0 {
		lw.status = http.StatusOK
	}
	lw.mu.Unlock()
	return lw.pw.Write(b)
}
func (lw *liveSSEWriter) WriteHeader(code int) {
	lw.mu.Lock()
	lw.status = code
	lw.mu.Unlock()
}
func (lw *liveSSEWriter) Status() int {
	lw.mu.RLock()
	defer lw.mu.RUnlock()
	return lw.status
}
func (lw *liveSSEWriter) Flush() {}

// anthropicFromOpenAICompletion — OpenAI 非流式响应 → Anthropic 消息
func anthropicFromOpenAICompletion(oai map[string]any) anthropicMessageResponse {
	model := stringValue(oai["model"])
	id := "msg_" + strings.ReplaceAll(randomUUID(), "-", "")
	resp := anthropicMessageResponse{
		ID:         id,
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    []anthropicContentBlock{},
		StopReason: "end_turn",
		Usage:      anthropicUsage{},
	}
	choices, _ := oai["choices"].([]any)
	if len(choices) == 0 {
		return resp
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	content := stringValue(message["content"])
	if content != "" {
		resp.Content = append(resp.Content, anthropicContentBlock{Type: "text", Text: content})
	}
	if calls, ok := message["tool_calls"].([]any); ok {
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			fn, _ := call["function"].(map[string]any)
			name := strings.TrimSpace(stringValue(fn["name"]))
			if name == "" {
				continue
			}
			callID := strings.TrimSpace(stringValue(call["id"]))
			if callID == "" {
				callID = "toolu_" + shortID(20)
			}
			var input map[string]any
			argsStr := stringValue(fn["arguments"])
			if err := json.Unmarshal([]byte(argsStr), &input); err != nil || input == nil {
				input = map[string]any{}
			}
			resp.Content = append(resp.Content, anthropicContentBlock{
				Type: "tool_use", ID: callID, Name: name, Input: input,
			})
		}
	}
	switch stringValue(choice["finish_reason"]) {
	case "tool_calls":
		resp.StopReason = "tool_use"
	case "length":
		resp.StopReason = "max_tokens"
	case "stop":
		resp.StopReason = "end_turn"
	}
	usage, _ := oai["usage"].(map[string]any)
	resp.Usage.InputTokens = intValue(usage["prompt_tokens"])
	resp.Usage.OutputTokens = intValue(usage["completion_tokens"])
	return resp
}

func intValue(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

// writeAnthropicError — 标准 Anthropic 错误结构
func writeAnthropicError(w http.ResponseWriter, status int, errType string, message string) {
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
}

func writeAnthropicErrorFromOpenAI(w http.ResponseWriter, status int, payload map[string]any) {
	errPayload := mapValue(payload["error"])
	if errPayload == nil {
		errPayload = payload
	}
	msg := stringValue(errPayload["message"])
	if strings.TrimSpace(msg) == "" {
		msg = "request failed"
	}
	errType := strings.TrimSpace(stringValue(errPayload["type"]))
	if errType == "" {
		errType = "api_error"
	}
	if code := strings.TrimSpace(stringValue(errPayload["code"])); code != "" {
		errType = firstNonEmpty(errType, code)
	}
	writeAnthropicError(w, status, errType, msg)
}

// rWithBody — 构造带 body 的请求副本（复用 chat 链路用）
func rWithBody(r *http.Request, body map[string]any) *http.Request {
	raw, _ := json.Marshal(body)
	clone := r.Clone(r.Context())
	clone.Body = ioNopCloserBytes(raw)
	clone.ContentLength = int64(len(raw))
	return clone
}

// responseRecorder — 捕获内部 handler 响应
type responseRecorder struct {
	header http.Header
	body   strings.Builder
	status int
}

func (rec *responseRecorder) Header() http.Header { return rec.header }
func (rec *responseRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.body.Write(b)
}
func (rec *responseRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
}

// Flush — 实现 http.Flusher（内部流式链路要求；响应被整体捕获，flush 为 no-op）
func (rec *responseRecorder) Flush() {}

var _ = fmt.Sprintf
var _ = time.Now
