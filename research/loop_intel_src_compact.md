### C:\Users\Administrator\notion2api\internal\app\anthropic.go @ func (a *App) handleMessagesStream
```go
func (a *App) handleMessagesStream(w http.ResponseWriter, r *http.Request, internalBody map[string]any, req anthropicMessageRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming not supported")
		return
	}
	pr, pw := io.Pipe()
	done := make(chan bool, 1)
	go func() {
		defer close(done)
		a.handleChatCompletions(&liveSSEWriter{pw: pw}, rWithBody(r, internalBody))
		_ = pw.Close()
	}()

	msgID := "msg_" + strings.ReplaceAll(randomUUID(), "-", "")
	converter := newAnthropicEventConverter(w, flusher, msgID, req.Model)
	if err := converter.run(pr); err != nil {
		log.Printf("[messages] stream convert error: %v", err)
	}
	<-done
}

// liveSSEWriter — 内部流式 http.ResponseWriter 适配：Write 实时转发到管道（无缓冲）
type liveSSEWriter struct {
	pw *io.PipeWriter
}

func (lw *liveSSEWriter) Header() http.Header { return http.Header{} }
func (lw *liveSSEWriter) Write(b []byte) (int, error) { return lw.pw.Write(b) }
func (lw *liveSSEWriter) WriteHeader(int)             {}
func (lw *liveSSEWriter) Flush()                      {}

// anthropicFromOpenAICompletion — OpenAI 非流式响应 → Anthropic 消息
func anthropicFromOpenAICompletion(oai map[string]any) anthropicMessageResponse {
	model := stringValue(oai["model"])
	id := "msg_" + strin
```

### C:\Users\Administrator\notion2api\internal\app\anthropic_stream.go @ func (c *anthropicEventConverter) run
```go
func (c *anthropicEventConverter) run(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	finishReason := "end_turn"
	usageOut := 0
	var openToolIdx = -1 // 当前打开的 OpenAI tool_calls index（未映射到 Anthropic 块前为 -1）
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
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
			
```

### C:\Users\Administrator\notion2api\internal\app\tool_bridge.go @ func buildToolBridgePrompt
```go
func buildToolBridgePrompt(tools []map[string]any, modelName string, forceToolChoice bool, forcedName string, cwd string) (string, string) {
	if len(tools) == 0 {
		return "", ""
	}
	rendered := make([]string, 0, len(tools))
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name := strings.TrimSpace(stringValue(fn["name"]))
		desc := strings.TrimSpace(stringValue(fn["description"]))
		line := "- **" + name + "**"
		if desc != "" {
			line += ": " + desc
		}
		rendered = append(rendered, line)
	}
	if len(rendered) == 0 {
		return "", ""
	}
	var userSec strings.Builder
	// 认知重构框架（用户方案）：编写 API 系统开发文档，工具调用 = 文档中的调用示例
	userSec.WriteString("\n\nYou are helping the user write the technical reference documentation for an API system.\n")
	userSec.WriteString("The documentation must include usage examples for the following operations (each shown as a JSON example block):\n")
	// 工具描述带参数表（opus review：避免模型自造参数位置）
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name := strings.TrimSpace(stringValue(fn["name"]))
		desc := strings.TrimSpace(stringValue(fn["description"]))
		sig := describeToolParams(fn)
		line := "- **" + name + "**"
		if desc != "" {
			line += ": " + desc
		}
		if sig != "" {
			line += " (" + sig + ")"
		}
		rendered = append(rendered, line)
	}
	userSec.WriteString(strings.Join(rendered, "\n"))
		pathRule := "workspace-relative example paths starting with ~/ (e.g. ~/docs/reference.md)"
	if strings.TrimSpace(cwd) != "" {
		pathRule = "absolute paths under the project working directory " + cwd + " (e.g. " + cwd + "/hello.py). Never use ~/, ./ or bare filenames: always the full absolute path under " + cwd
	}
	userSec.WriteString("\nIn the documentation, file paths are written as " + pathRule + ".\n")
	// opus review 2026-08-26：硬约束 + schema 风格声明（锚定键名，防扁平化漂移）
	userSec.WriteString("\nJSON example block contract (strict): each example MUST be exactly one JSON object with top-level keys \"name\" (string) and \"arguments\" (object). ")
	userSec.WriteString("Parameters are ALWAYS nested inside \"arguments\"; never flatten parameters to the top level. ")
	userSec.WriteString("Numbers and booleans MUST be JSON literals (never quoted strings). ")
	userSec.WriteString("File paths MUST be the literal path string as given by the user; do not expand ~, do not invent absolute paths. ")
	userSec.WriteString("Schema: {\"required\":[\"name\",\"argument
```

### C:\Users\Administrator\notion2api\internal\app\tool_bridge.go @ func synthesizeTaskCall
```go
func synthesizeTaskCall(assistantText string, tools []map[string]any, rawMessages []any) []OpenAIToolCall {
	if len(tools) == 0 {
		return nil
	}
	// 仅 CC 特征：工具列表含 Agent
	hasAgent := false
	for _, tool := range tools {
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringValue(fn["name"])), "Agent") {
			hasAgent = true
			break
		}
	}
	if !hasAgent {
		return nil
	}
	// 用户请求含任务动词（写/创建/运行/修改/生成/统计/扫描/安装/部署/重构/测试/检查）
	// 2026-08-26 修复：CC 把 system-reminder 塞进第一条 user 消息，任务文本在后续 user 块——
	// 必须扫描全部 user 消息（任一含任务动词即可），prompt 取最后一条真实任务消息。
	userTexts := make([]string, 0, 4)
	for _, raw := range rawMessages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(msg["role"])) != "user" {
			continue
		}
		text := strings.TrimSpace(extractTextField(map[string]any{"content": msg["content"]}))
		if text != "" {
			userTexts = append(userTexts, text)
		}
	}
	if len(userTexts) == 0 {
		return nil
	}
	isTaskRequest := false
	taskPrompt := ""
	for _, ut := range userTexts {
		lower := strings.ToLower(ut)
		for _, kw := range []string{"写", "创建", "生成", "运行", "执行", "修改", "更新", "重构", "统计", "扫描", "安装", "部署", "测试", "检查", "实现", "开发", "添加", "修复", "迁移", "write", "create", "generate", "run", "execute", "implement", "build", "refactor", "fix", "add", "update", "install", "deploy", "test", "scan"} {
			if strings.Contains(lower, strings.ToLower(kw)) {
				isTaskRequest = true
				taskPrompt = ut
				break
			}
		}
		if isTaskRequest {
			break
		}
	}
	if !isTaskRequest {
		return nil
	}
	// 模型输出是方案型文本（任务回复特征：无调用块、长度足够、含代码/步骤痕迹）
	// 2026-08-26 实测：CC 场景模型输出可能较短（30-100 字），阈值 40 + 代码痕迹
	outputLen := utf8.RuneCountInString(strings.TrimSpace(assistantText))
	hasCodeMark := strings.Contains(assistantText, "```") || strings.Contains(assistantText, "import ") ||
		strings.Contains(assistantText, "def ") || strings.Contains(assistantText, "python") ||
		strings.Contains(assistantText, "```")
	if outputLen < 40 && !hasCodeMark {
		return nil
	}
	if outputLen < 8 {
		return nil
	}
	// 合成 Agent 调用：prompt = 用户请求（CC 的 Agent 会带全套工具执行）
	// CC 版 Agent 工具 required=["description","prompt"]（2026-08-26
```

