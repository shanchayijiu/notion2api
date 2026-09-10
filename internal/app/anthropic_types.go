package app

// anthropic_types.go — Anthropic Messages API 兼容层结构体（2026-08-26）
// 目的：Claude Code 等 Anthropic 客户端原生工具传递路径（/v1/messages），
// 绕开 openai_chat 适配层 tools=0 的问题（opus 设计 F-1）。

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicMessageRequest — POST /v1/messages 请求
type anthropicMessageRequest struct {
	Model                  string             `json:"model"`
	Messages               []anthropicMessage `json:"messages"`
	System                 any                `json:"system,omitempty"`
	Tools                  []anthropicTool    `json:"tools,omitempty"`
	ToolChoice             any                `json:"tool_choice,omitempty"`
	MaxTokens              *int               `json:"max_tokens,omitempty"`
	Temperature            *float64           `json:"temperature,omitempty"`
	StopSequences          []string           `json:"stop_sequences,omitempty"`
	Stream                 bool               `json:"stream,omitempty"`
	Metadata               map[string]any     `json:"metadata,omitempty"`
	DisableParallelToolUse bool               `json:"disable_parallel_tool_use,omitempty"`
	UseWebSearch           *bool              `json:"use_web_search,omitempty"`
	rawPayload             map[string]any     `json:"-"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string 或 []anthropicContentBlock
	Name    string `json:"name,omitempty"`
}

type anthropicContentBlock struct {
	Type      string         `json:"type"` // text / image / tool_use / tool_result
	Text      string         `json:"text,omitempty"`
	Source    map[string]any `json:"source,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     any            `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   any            `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// anthropicMessageResponse — 非流式响应
type anthropicMessageResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Model        string                  `json:"model"`
	Content      []anthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// parseAnthropicSystem — system 字段（string 或 [{"type":"text","text":...}]）→ 拼接文本
func parseAnthropicSystem(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if strings.TrimSpace(stringValue(m["text"])) != "" {
					parts = append(parts, strings.TrimSpace(stringValue(m["text"])))
				}
			} else if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// anthropicToolChoiceToOpenAI — tool_choice 映射：
// auto→auto；any→required；{type:tool,name}→指定；none/stop→none。
// Anthropic 的 tool_choice 是受限 union；未知形状不能静默回退为 auto。
func anthropicToolChoiceToOpenAI(raw any) (string, bool, error) {
	if raw == nil {
		return "auto", false, nil
	}
	switch v := raw.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "any":
			return "required", true, nil
		case "auto":
			return "auto", true, nil
		case "none", "stop":
			return "none", true, nil
		default:
			return "", false, fmt.Errorf("unsupported tool_choice %q", v)
		}
	case map[string]any:
		typeName := strings.ToLower(strings.TrimSpace(stringValue(v["type"])))
		switch typeName {
		case "tool":
			name := strings.TrimSpace(stringValue(v["name"]))
			if name == "" {
				return "", false, fmt.Errorf("tool_choice.type=tool requires name")
			}
			return name, true, nil
		case "none", "stop":
			return "none", true, nil
		case "any":
			return "required", true, nil
		case "auto":
			return "auto", true, nil
		default:
			return "", false, fmt.Errorf("unsupported tool_choice type %q", typeName)
		}
	default:
		return "", false, fmt.Errorf("tool_choice must be a string or object")
	}
}

func anthropicNestedDisableParallelToolUse(raw any) bool {
	choice, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	value, _ := choice["disable_parallel_tool_use"].(bool)
	return value
}

// anthropicToolsToOpenAI — tools 转换：input_schema → parameters
func anthropicToolsToOpenAI(tools []anthropicTool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if strings.TrimSpace(stringValue(schema["type"])) == "" {
			schema["type"] = "object"
		}
		if _, ok := schema["properties"]; !ok {
			schema["properties"] = map[string]any{}
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  schema,
			},
		})
	}
	return out
}

// parseAnthropicMessages — messages → 内部 OpenAI 格式 messages（含 tool_use/tool_result 转换）
func parseAnthropicMessages(messages []anthropicMessage) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		if s, ok := content.(string); ok {
			if strings.TrimSpace(s) == "" {
				continue
			}
			out = append(out, map[string]any{"role": role, "content": s})
			continue
		}
		blocks, ok := content.([]any)
		if !ok {
			continue
		}
		var textParts []string
		var toolCalls []map[string]any
		var toolResults []map[string]any
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(stringValue(block["type"])) {
			case "text":
				if s := strings.TrimSpace(stringValue(block["text"])); s != "" {
					textParts = append(textParts, s)
				}
			case "tool_use":
				toolCalls = append(toolCalls, map[string]any{
					"id":   strings.TrimSpace(stringValue(block["id"])),
					"type": "function",
					"function": map[string]any{
						"name":      strings.TrimSpace(stringValue(block["name"])),
						"arguments": jsonString(block["input"]),
					},
				})
			case "tool_result":
				toolResults = append(toolResults, map[string]any{
					"tool_call_id": strings.TrimSpace(stringValue(block["tool_use_id"])),
					"content":      flattenAnthropicContent(block["content"]),
					"is_error":     block["is_error"] == true,
				})
			case "image":
				textParts = append(textParts, "[image omitted]")
			}
		}
		// 合并同角色：文本 + 工具调用放一个 assistant 消息；tool_result 转 tool 消息
		if role == "user" && len(toolResults) > 0 {
			for _, tr := range toolResults {
				m := map[string]any{"role": "tool"}
				for k, v := range tr {
					m[k] = v
				}
				if strings.TrimSpace(stringValue(m["content"])) == "" {
					m["content"] = "[empty tool result]"
				}
				out = append(out, m)
			}
			continue
		}
		msgMap := map[string]any{"role": role}
		if len(toolCalls) > 0 {
			msgMap["tool_calls"] = toolCalls
			msgMap["content"] = strings.Join(textParts, "\n")
		} else if len(textParts) > 0 {
			msgMap["content"] = strings.Join(textParts, "\n")
		} else {
			continue
		}
		out = append(out, msgMap)
	}
	return out
}

// flattenAnthropicContent — tool_result.content（string 或块数组）→ 文本
func flattenAnthropicContent(raw any) string {
	if s, ok := raw.(string); ok {
		return s
	}
	if blocks, ok := raw.([]any); ok {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if m, ok := b.(map[string]any); ok {
				if t := strings.TrimSpace(stringValue(m["text"])); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func jsonString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

var _ = anthropicMessageResponse{}

// filterSubagentTools — 屏蔽 CC 子代理类工具（2026-08-26 C3，opus 决策）：
// Agent/SendMessage/AddTaskNotificationTool 会让 CC 创建异步子代理（耗配额、链路长、易断），
// 屏蔽后模型只能输出 Write/Bash/Read 等常规工具调用 → CC 本地直接执行。
var subagentToolNames = map[string]bool{
	"Agent":                   true,
	"SendMessage":             true,
	"AddTaskNotificationTool": true,
	"AddTaskOutput":           true,
}

func filterSubagentTools(tools []map[string]any) []map[string]any {
	if len(tools) == 0 {
		return tools
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if subagentToolNames[strings.TrimSpace(stringValue(fn["name"]))] {
			continue
		}
		out = append(out, tool)
	}
	return out
}
