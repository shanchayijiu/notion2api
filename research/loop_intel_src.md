### C:\Users\Administrator\notion2api\internal\app\anthropic.go @ func (a *App) handleMessages
```go
func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	var req anthropicMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if len(req.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "messages must be a non-empty array")
		return
	}
	// 转换请求到内部 OpenAI 形
```

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

### C:\Users\Administrator\notion2api\internal\app\main.go @ func (a *App) writeChatCompletionLiveStream
```go
func (a *App) writeChatCompletionLiveStream(w http.ResponseWriter, r *http.Request, request PromptRunRequest, modelID string, includeUsage bool, conversationID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is not supported by this response writer", "api_error", "stream_unsupported")
		return
	}

	completionID := "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", "")
	created := time.Now().Unix()
	var emittedVisibleText strings.Builder
	var emittedReasoning strings.Builder
	var streamSieve = toolStreamSieve{lastFeed: time.Now()}
	// stop 序列存在时走缓冲模式（S4 截断必须先于下发；实时增量无法中途撤回）
	stopBuffered := len(request.StopSequences) > 0
	var stopBuffer strings.Builder
	warmupSent := false
	const reasoningHeartbeat = "\u200b"
	var writeMu sync.Mutex
	headersSent := false
	safeWriteData := func(payload any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeSSEData(w, flusher, payload)
	}
	safeWriteDone := func() {
		writeMu.Lock()
		defer writeMu.Unlock()
		writeSSEDone(w, flusher)
	}
	startStream := func() error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if headersSent {
			return nil
		}
		headersSent = true
		prepareOpenAISSEHeaders(w)
		a.markConversationEnvelope(conversationID, "", completionID)
		return writeSSEData(w, flusher, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamDeltaChoice(0, map[string]any{"role": "assistant"}),
		}, usageNull(includeUsage)))
	}
	emitContent := func(part string) error {
		if part == "" {
			return nil
		}
		if stopBuffered {
			// stop 缓冲模式：整段收集，终止时统一 sanitize + 截断后一次下发
			stopBuffer.WriteString(part)
			return nil
		}
		// toolStreamSieve：半截工具标记缓冲，不泄漏到正文
		if safe := streamSieve.feed(part); safe != "" {
			if err := startStream(); err != nil {
				return err
			}
			emittedVisibleText.WriteString(safe)
			return safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{
				buildChatStreamDeltaChoice(0, map[string]any{"content": safe}),
			}, usageNull(includeUsage)))
		}
		return nil
	}
	emitReasoning := func(part string) error {
		if part == "" || request.SuppressReasoningOutput {
			return nil
		}
		if err := startStream(); err != nil {
			return err
		}
		emittedReasoning.WriteString(part)
		return safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{

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

### C:\Users\Administrator\notion2api\internal\app\tool_bridge.go @ func filterCallsToAvailable
```go
func filterCallsToAvailable(calls []OpenAIToolCall, tools []map[string]any) []OpenAIToolCall {
	if len(calls) == 0 || len(tools) == 0 {
		return calls
	}
	type toolInfo struct {
		name        string
		description string
	}
	catalog := make([]toolInfo, 0, len(tools))
	available := map[string]bool{}
	for _, tool := range tools {
		if fn, ok := tool["function"].(map[string]any); ok {
			n := strings.TrimSpace(stringValue(fn["name"]))
			available[strings.ToLower(n)] = true
			catalog = append(catalog, toolInfo{name: n, description: strings.TrimSpace(stringValue(fn["description"]))})
		}
	}
	// 描述前缀 → 工具名映射（模型把 description 当名字时的归一）
	resolveName := func(callName string) string {
		lower := strings.ToLower(strings.TrimSpace(callName))
		if available[lower] {
			return callName
		}
		lowerDesc := strings.ToLower(lower)
		for _, ti := range catalog {
			desc := strings.ToLower(ti.description)
			// 模型 name 是某工具 description 的前缀（≥8 字符）或 description 以模型 name 开头
			if len(lowerDesc) >= 8 && (strings.HasPrefix(desc, lowerDesc) || strings.HasPrefix(lowerDesc, desc)) {
				return ti.name
			}
		}
		// 退化：name 是工具名的子串（如 "Agent tool" 含 "agent"）
		for _, ti := range catalog {
			if strings.Contains(lower, strings.ToLower(ti.name)) {
				return ti.name
			}
		}
		return ""
	}
	out := make([]OpenAIToolCall, 0, len(calls))
	for _, call := range calls {
		if resolved := resolveName(call.Function.Name); resolved != "" {
			call.Function.Name = resolved
			out = append(out, call)
		}
	}
	return out

```

### C:\Users\Administrator\notion2api\internal\app\workspace_rotation.go @ func (r *WorkspaceRotator) Rotate
```go
func (r *WorkspaceRotator) Rotate(ctx context.Context, cfg AppConfig, session SessionInfo) (SessionInfo, error) {
	accountEmail := strings.TrimSpace(session.ProbePath)
	if accountEmail == "" {
		accountEmail = findAccountEmailForSession(cfg, session)
	}
	email := strings.TrimSpace(session.UserEmail)
	if email == "" {
		email = accountEmail
	}
	// 账号每日冷却检查（429 后标记，24h 自动恢复）
	if active, until := accountDailyCooldownActive(cfg, email); active {
		return session, fmt.Errorf("workspace rotation: account cooling down until %s (daily limit; auto recovers)", until.Format(time.RFC3339))
	}
	if r.store != nil {
		recent, err := r.store.LoadSpaceLifecycles(email, spaceStatusActive)
		if err == nil && len(recent) > 0 {
			if last := parseLifecycleTime(recent[0].CreatedAt); !last.IsZero() && time.Since(last) < rotateMinInterval {
				return session, fmt.Errorf("workspace rotation throttled: last create at %s, min interval %s",
					last.Format(time.RFC3339), rotateMinInterval)
			}
		}
	}
	client := newNotionAIClient(session, cfg, accountEmail)

	// 轮换是恢复动作，用独立更长超时（不继承请求 60s 预算；createspace 响应可能 30-90s）
	rotateCtx, cancel := context.WithTimeout(context.Background(), rotateHTTPTimeout)
	defer can
```