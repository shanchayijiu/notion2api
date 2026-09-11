package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// generationParams captures the OpenAI Chat Completions generation knobs that
// the upstream Notion provider cannot natively express.
//
// Policy (Phase 1, P0-1): never silently drop a parameter that changes the
// response contract. Mappable knobs are enforced locally (max output tokens,
// parallel tool calls, response format); unsupported but harmless sampling
// hints are accepted and surfaced to the caller via a response header + log so
// the caller is not misled. Contract-changing requests we cannot honour (n > 1)
// are rejected with a clear invalid_request_error.
type generationParams struct {
	Temperature       *float64
	TopP              *float64
	MaxOutputTokens   int
	N                 int
	ResponseFormat    string
	JSONSchema        string
	ParallelToolCalls *bool
	Unsupported       []string
}

func parseGenerationParams(body chatCompletionsRequestBody) (generationParams, error) {
	params := generationParams{N: 1}
	if body.N != nil {
		if *body.N < 1 {
			return params, fmt.Errorf("n must be greater than or equal to 1")
		}
		params.N = *body.N
	}
	if params.N > 1 {
		return params, fmt.Errorf("n=%d is not supported: this bridge returns a single choice", params.N)
	}
	if body.Temperature != nil {
		if *body.Temperature < 0 || *body.Temperature > 2 {
			return params, fmt.Errorf("temperature must be between 0 and 2")
		}
		params.Temperature = body.Temperature
		params.Unsupported = append(params.Unsupported, "temperature")
	}
	if body.TopP != nil {
		if *body.TopP < 0 || *body.TopP > 1 {
			return params, fmt.Errorf("top_p must be between 0 and 1")
		}
		params.TopP = body.TopP
		params.Unsupported = append(params.Unsupported, "top_p")
	}
	if body.MaxCompletionTokens != nil && *body.MaxCompletionTokens > 0 {
		params.MaxOutputTokens = *body.MaxCompletionTokens
	} else if body.MaxTokens != nil && *body.MaxTokens > 0 {
		params.MaxOutputTokens = *body.MaxTokens
	}
	if body.ParallelToolCalls != nil {
		params.ParallelToolCalls = body.ParallelToolCalls
	}
	format, schema, err := parseResponseFormat(body.ResponseFormat)
	if err != nil {
		return params, err
	}
	params.ResponseFormat = format
	params.JSONSchema = schema
	for _, item := range []struct {
		set  bool
		name string
	}{
		{body.PresencePenalty != nil, "presence_penalty"},
		{body.FrequencyPenalty != nil, "frequency_penalty"},
		{body.Seed != nil, "seed"},
		{body.Logprobs != nil, "logprobs"},
		{body.TopLogprobs != nil, "top_logprobs"},
		{strings.TrimSpace(body.ReasoningEffort) != "", "reasoning_effort"},
		{strings.TrimSpace(body.User) != "", "user"},
	} {
		if item.set {
			params.Unsupported = append(params.Unsupported, item.name)
		}
	}
	return params, nil
}

func parseResponseFormat(raw any) (string, string, error) {
	obj := decodeJSONObjectAny(raw)
	if obj == nil {
		return "", "", nil
	}
	formatType := strings.ToLower(strings.TrimSpace(stringValue(obj["type"])))
	switch formatType {
	case "", "text":
		return "text", "", nil
	case "json_object":
		return "json_object", "", nil
	case "json_schema":
		schemaObj := mapValue(obj["json_schema"])
		if schemaObj == nil {
			return "", "", fmt.Errorf("response_format.json_schema is required when type is json_schema")
		}
		encoded := ""
		if schema := mapValue(schemaObj["schema"]); schema != nil {
			if data, err := json.Marshal(schema); err == nil {
				encoded = string(data)
			}
		}
		return "json_schema", encoded, nil
	default:
		return "", "", fmt.Errorf("unsupported response_format.type %q", formatType)
	}
}

// responseFormatInstruction returns a hidden-prompt instruction that nudges the
// upstream model towards the requested output format. It is best-effort: the
// bridge cannot enforce upstream compliance, but it avoids accepting the
// parameter and doing nothing at all.
func (p generationParams) responseFormatInstruction() string {
	switch p.ResponseFormat {
	case "json_object":
		return "Response format requirement: reply with a single valid JSON object only. Do not wrap it in prose or markdown code fences."
	case "json_schema":
		if p.JSONSchema != "" {
			return "Response format requirement: reply with a single valid JSON value that conforms to this JSON Schema: " + p.JSONSchema + ". Do not wrap it in prose or markdown code fences."
		}
		return "Response format requirement: reply with a single valid JSON object only."
	default:
		return ""
	}
}

// truncateTextToTokens cuts text so its estimated token count does not exceed
// maxTokens, on a rune boundary. Returns (text, true) when a cut happened.
func truncateTextToTokens(text string, maxTokens int) (string, bool) {
	if maxTokens <= 0 || text == "" {
		return text, false
	}
	if estimateTokens(text) <= maxTokens {
		return text, false
	}
	maxRunes := maxTokens * 4
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text, false
	}
	return strings.TrimRight(string(runes[:maxRunes]), " \t\r\n"), true
}

// applyMaxOutputTokens enforces max_tokens / max_completion_tokens locally and
// marks the result truncated so finish_reason becomes "length".
func applyMaxOutputTokens(result InferenceResult, maxTokens int) InferenceResult {
	if maxTokens <= 0 {
		return result
	}
	truncatedText, truncated := truncateTextToTokens(result.Text, maxTokens)
	if truncated {
		result.Text = truncatedText
		result.Truncated = true
	}
	return result
}

func numericFloatField(raw any) *float64 {
	value, ok := numericFloatValue(raw)
	if !ok {
		return nil
	}
	return &value
}

func numericFloatValue(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	}
	return 0, false
}

func numericIntField(raw any) *int {
	if value, ok := numericInt64Value(raw); ok {
		converted := int(value)
		return &converted
	}
	return nil
}

func numericInt64Field(raw any) *int64 {
	if value, ok := numericInt64Value(raw); ok {
		return &value
	}
	return nil
}

func numericInt64Value(raw any) (int64, bool) {
	switch value := raw.(type) {
	case float64:
		return int64(value), true
	case float32:
		return int64(value), true
	case int:
		return int64(value), true
	case int64:
		return value, true
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func numericBoolField(raw any) *bool {
	if value, ok := parseBoolField(raw); ok {
		return &value
	}
	return nil
}

// applyGenerationParamHeaders surfaces accepted-but-not-forwarded parameters so
// callers are never silently misled about what the bridge honoured.
func applyGenerationParamHeaders(w http.ResponseWriter, params generationParams) {
	if w == nil || len(params.Unsupported) == 0 {
		return
	}
	names := strings.Join(params.Unsupported, ",")
	w.Header().Set("X-Notion2API-Unsupported-Params", names)
	log.Printf("[generation-params] accepted but not forwarded upstream: %s", names)
}

// applyParallelToolCallsLimit enforces parallel_tool_calls=false by keeping only
// the first emitted tool call.
func applyParallelToolCallsLimit(calls []OpenAIToolCall, parallel *bool) []OpenAIToolCall {
	if parallel == nil || *parallel || len(calls) <= 1 {
		return calls
	}
	return calls[:1]
}
