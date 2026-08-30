package app

// anthropic_stream.go — Anthropic SSE 事件转换器（2026-08-26）
// 读内部 OpenAI chat/completions SSE 流 → 写 Anthropic Messages SSE 事件。
// 骨架：message_start → 内容块（text/tool_use）→ message_delta → message_stop
// 工具块：OpenAI tool_calls 分片首片（带 id/name）→ content_block_start{tool_use}
//        ；后续 arguments 增量 → input_json_delta；结束 → content_block_stop

import (
	"bufio"
	"sync"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// newPipeWriter — 内存管道 writer（流式转换：内部 SSE → 转换器）
func newPipeWriter() (io.Reader, *io.PipeWriter) {
	pr, pw := io.Pipe()
	return pr, pw
}

// ioNopCloserBytes — bytes.Reader 包成 io.ReadCloser
func ioNopCloserBytes(b []byte) io.ReadCloser {
	return io.NopCloser(strings.NewReader(string(b)))
}

type anthropicEventConverter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	msgID    string
	model    string
	blockIdx int
	started  bool
	writeMu  sync.Mutex // 2026-08-26 修复：ping goroutine 与主循环并发写响应（CC 卡死根因）
	// upstream — 内部链路真实状态码载体（P1-1）：上游错误在 SSE headers 发出前
	// 写的是 JSON 错误体（非 data: 行），converter 据此发 error 事件而非空成功骨架。
	upstream *liveSSEWriter
	// lastPlainLine — 最近一条非 data: 文本行（截断），用于从 JSON 错误体提取 message
	lastPlainLine string
}

func newAnthropicEventConverter(w http.ResponseWriter, flusher http.Flusher, msgID string, model string) *anthropicEventConverter {
	return &anthropicEventConverter{w: w, flusher: flusher, msgID: msgID, model: model}
}

func (c *anthropicEventConverter) send(event string, payload any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt_Fprintf(c.w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt_Fprintf(c.w, "data: %s\n\n", marshalJSON(payload)); err != nil {
		return err
	}
	c.flusher.Flush()
	return nil
}

func (c *anthropicEventConverter) run(reader io.Reader) error {
	startAt := time.Now()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	finishReason := "end_turn"
	usageOut := 0
	firstChunkAt := time.Time{}
	// 心跳：15s 无事件时发 ping（Anthropic 协议事件；防中间层/客户端超时断连）
	// P1-7 修复：ping 写失败 = 客户端已断连，停止心跳而不是继续空转。
	pingStop := make(chan struct{})
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingStop:
				return
			case <-ticker.C:
				if err := c.send("ping", map[string]any{"type": "ping"}); err != nil {
					log.Printf("[messages-stream] ping failed (client gone): %v", err)
					return
				}
			}
		}
	}()
	defer func() { close(pingStop); <-pingDone }()
	var openToolIdx = -1 // 当前打开的 OpenAI tool_calls index（未映射到 Anthropic 块前为 -1）
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			// P1-1：记录非 data: 行（SSE 注释 ":" 开头除外）——内部链路在上游失败时
			// 写的是 JSON 错误体，行首不带 data: 前缀，converter 需要据此识别错误
			if line != "" && !strings.HasPrefix(line, ":") {
				if len(line) > 400 {
					line = line[:400] + "..."
				}
				c.lastPlainLine = line
			}
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "[DONE]" {
			break
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if firstChunkAt.IsZero() {
			firstChunkAt = time.Now()
			log.Printf("[messages-stream] TTFT first upstream chunk after %s", firstChunkAt.Sub(startAt).Round(time.Millisecond))
		}
		// 错误行
		if errObj, ok := chunk["error"].(map[string]any); ok {
			msg := stringValue(errObj["message"])
			if msg == "" {
				msg = "stream error"
			}
			_ = c.send("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
			return nil
		}
		if u, ok := chunk["usage"].(map[string]any); ok && len(u) > 0 {
			usageOut = intValue(u["completion_tokens"])
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if !c.started {
			c.started = true
			_ = c.send("message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": c.msgID, "type": "message", "role": "assistant",
					"model": c.model, "content": []any{},
					"stop_reason": nil, "stop_sequence": nil,
					"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
				},
			})
		}
		// 角色首片（role: assistant）→ 不额外发事件
		// 文本增量
		if text, ok := delta["content"].(string); ok && text != "" {
			if openToolIdx >= 0 {
				_ = c.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": c.blockIdx - 1})
				openToolIdx = -1
			}
			_ = c.send("content_block_start", map[string]any{
				"type": "content_block_start", "index": c.blockIdx,
				"content_block": map[string]any{"type": "text", "text": ""},
			})
			_ = c.send("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": c.blockIdx,
				"delta": map[string]any{"type": "text_delta", "text": text},
			})
			_ = c.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": c.blockIdx})
			c.blockIdx++
		}
		// 工具调用分片
		if tcs, ok := delta["tool_calls"].([]any); ok && len(tcs) > 0 {
			for _, rawTC := range tcs {
				tc, _ := rawTC.(map[string]any)
				fn, _ := tc["function"].(map[string]any)
				idx := intValue(tc["index"])
				// 首片：带 id/name → 打开块
				if name := strings.TrimSpace(stringValue(fn["name"])); name != "" {
					if openToolIdx >= 0 {
						_ = c.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": c.blockIdx - 1})
						openToolIdx = -1
					}
					callID := strings.TrimSpace(stringValue(tc["id"]))
					if callID == "" {
						callID = "toolu_" + shortID(20)
					}
					_ = c.send("content_block_start", map[string]any{
						"type": "content_block_start", "index": c.blockIdx,
						"content_block": map[string]any{
							"type": "tool_use", "id": callID, "name": name, "input": map[string]any{},
						},
					})
					openToolIdx = idx
					// arguments 增量（首片可能带部分 arguments 或空）
					if args, ok := fn["arguments"].(string); ok && args != "" {
						_ = c.send("content_block_delta", map[string]any{
							"type": "content_block_delta", "index": c.blockIdx,
							"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
						})
					}
					c.blockIdx++
					continue
				}
				// 增量片：仅 arguments
				if args, ok := fn["arguments"].(string); ok && args != "" {
					blockIndex := c.blockIdx - 1
					if openToolIdx != idx {
						// 索引错位时用当前打开的块
						blockIndex = c.blockIdx - 1
					}
					_ = c.send("content_block_delta", map[string]any{
						"type": "content_block_delta", "index": blockIndex,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
					})
				}
			}
		}
		// finish_reason
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			switch fr {
			case "tool_calls":
				finishReason = "tool_use"
			case "length":
				finishReason = "max_tokens"
			case "stop":
				finishReason = "end_turn"
			}
		}
	}
	// P1-2 修复：scanner 出错（>16MB 行等）必须显式上报并终止为错误事件，
	// 不能静默走「正常收尾」——否则截断被吞、客户端拿到残缺的"成功"流。
	if scanErr := scanner.Err(); scanErr != nil {
		log.Printf("[messages-stream] upstream SSE scan error: %v", scanErr)
		_ = c.send("error", map[string]any{"type": "error", "error": map[string]any{
			"type": "api_error", "message": "internal stream decode error",
		}})
		return scanErr
	}
	// 收尾：未收到任何 chunk 时的空流处理（P1-1）：
	// 内部链路在上游失败时写的是 JSON 错误体（非 data: 行），且状态码 >=400 —
	// 必须转成 error 事件，而不是给客户端发"空的成功消息"（CC 会静默继续/重试风暴）。
	if !c.started {
		if c.upstream != nil && c.upstream.status >= 400 {
			log.Printf("[messages-stream] upstream failed status=%d before SSE start", c.upstream.status)
			_ = c.send("error", map[string]any{"type": "error", "error": map[string]any{
				"type": "api_error", "message": anthropicUpstreamErrorMessage(c.upstream.status, c.lastPlainLine),
			}})
			return fmt.Errorf("upstream error status %d", c.upstream.status)
		}
		var errObj map[string]any
		if c.lastPlainLine != "" && json.Unmarshal([]byte(c.lastPlainLine), &errObj) == nil {
			if e, ok := errObj["error"].(map[string]any); ok {
				msg := stringValue(e["message"])
				if msg == "" {
					msg = "upstream error"
				}
				_ = c.send("error", map[string]any{"type": "error", "error": map[string]any{
					"type": "api_error", "message": msg,
				}})
				return fmt.Errorf("upstream error: %s", msg)
			}
		}
		// 真正空流（无错误信号）才发骨架
		c.started = true
		_ = c.send("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": c.msgID, "type": "message", "role": "assistant",
				"model": c.model, "content": []any{},
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		})
	}
	// 收尾：关掉打开的块 → message_delta → message_stop
	if openToolIdx >= 0 {
		_ = c.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": c.blockIdx - 1})
		openToolIdx = -1
	}
	_ = c.send("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{"stop_reason": finishReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": usageOut},
	})
	_ = c.send("message_stop", map[string]any{"type": "message_stop"})
	return nil
}

// fmt_Fprintf — 局部别名（避免 import fmt 冲突）
func fmt_Fprintf(w io.Writer, format string, args ...any) (int, error) {
	return fmt.Fprintf(w, format, args...)
}

// anthropicUpstreamErrorMessage — 从内部链路状态码/JSON 错误体提炼 Anthropic 错误消息（P1-1）
func anthropicUpstreamErrorMessage(status int, plainLine string) string {
	if plainLine != "" {
		var obj map[string]any
		if json.Unmarshal([]byte(plainLine), &obj) == nil {
			if e, ok := obj["error"].(map[string]any); ok {
				if msg := strings.TrimSpace(stringValue(e["message"])); msg != "" {
					return msg
				}
			}
		}
	}
	return fmt.Sprintf("upstream request failed with status %d", status)
}