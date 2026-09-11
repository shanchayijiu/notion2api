# ARCHITECTURE_REVIEW — Notion2API 竞赛级架构审计

> Phase 0 交付物。**只做审计，不改业务代码。**
> 审计基线：`main` @ `4c3e3d7`（`audit: preserve compatibility protocol semantics`）。
> 审计方式：静态源码通读 + 既有单测运行。
> 验证状态：`go build ./...` / `go vet ./...` / `go test ./internal/app -count=1` 全绿；
> 审计机器上**没有运行中的服务实例与配置**，因此**未做 live 验证**（TTFB/latency 等为推断，已在文中标注）。

---

## 0. 审计范围与方法

- 范围：`internal/app/*.go`（Go 网关、Provider、账号池、协议适配、Admin）、`frontend/`、`.github/workflows/`、`config*.json`。
- 方法：
  1. 通读请求入口 → 调度 → Provider → 上游 → 协议渲染全链路；
  2. 对每个结论要求 `file:line` 证据；
  3. 区分「代码事实」与「需 live 确认」；
  4. 用技能库（2api-bridge）的成熟判据交叉验证（真流式、prompt bypass、wire 兼容）。
- 明确不做：任何业务代码修改、任何"看起来高级"的无用抽象。

### 本次锁定的目标（用户拍板）

| 项 | 决策 |
|---|---|
| 第一优先客户端 | **OpenAI Python SDK + `/v1/chat/completions`** |
| Responses / Anthropic | 本期**只保证不回归**，不优先扩功能 |
| Tool 能力 | **完整 Tool Call 协议闭环**（服务端产出标准 `tool_calls` + tool 结果续轮）；**不做**服务端任意工具执行平台 |
| 延迟默认值 | **保留现有默认**，只新增开关 |
| Phase 0 交付 | 仅本文档 |

---

## 1. 当前架构（现状图）

```
Client (OpenAI SDK / CC / Cline / OpenWebUI)
  │
  ▼
App.ServeHTTP  (internal/app/main.go:3089)
  │  路由 (main.go:3161-3182)
  ├── POST /v1/chat/completions → handleChatCompletions      main.go:1716
  ├── POST /v1/responses        → handleResponses            main.go:2000
  ├── POST /v1/messages         → handleMessages             anthropic.go:35
  ├── GET  /v1/models           → serveModels                main.go:1168
  └── /admin/* (WebUI)
  │
  ▼
wire 解码 / normalize
  ├── decodeChatCompletionsRequestBodyFromRaw  main.go:1631
  ├── normalizeChatInputFromParts              openai.go:125
  └── 续聊匹配 / 工具桥注入                      main.go:1792, 1818
  │
  ▼
writeChatCompletionLiveStream (流)  main.go:2288   |   runPrompt (非流)  main.go:1841
  │
  ▼
runPromptWithAccountPool / WithSink   request_dispatch.go:488 / 708
  ├── 健康探测 (45s/账号缓存)           request_dispatch.go:307-316
  ├── 选号 / 半开 / pinned             account_pool.go:164-222
  └── context.WithTimeout(r.Context()) request_dispatch.go:714
  │
  ▼
runPromptWithSessionWithSink          account_pool.go:310
  │
  ▼
newNotionAIClient (每请求新建 client，transport 复用)  notion_client.go:921-945
  │
  ▼
preparePromptRequest                  notion_client.go:4167
  ├── ensureSessionLiveMetadata (通常 0 网络)  notion_client.go:766
  ├── 续聊 draft/scaffold (+3 往返)            notion_client.go:4204, 4214
  └── buildInferencePayload                    notion_client.go:3970
  │
  ▼
runInferenceTranscript (NDJSON, 逐行)  notion_client.go:1336 / 2804
  │
  ▼
Notion AI upstream (https://www.notion.so)
```

**关键事实**：目前没有 Provider 抽象层。Notion 私有 wire（NDJSON transcript/patch）直接耦合进 `notion_client.go`，协议渲染（OpenAI/Anthropic）直接耦合进 `main.go`/`openai.go`。`InferenceStreamSink` 只有 4 个回调（`stream_sink.go:3-8`），**没有规范化事件模型**。

---

## 2. 逐条回答（Q1–Q18）

### Q1. `/v1/chat/completions` 完整请求链路是什么？

`ServeHTTP` 路由 `main.go:3174` → `handleChatCompletions` `main.go:1716`：

1. `decodeBodyRaw` → typed+raw 解码 `main.go:1631`（typed 与 raw payload 都保留）。
2. SillyTavern 探测/分流 `main.go:1727-1737`。
3. `normalizeChatInputFromParts` `openai.go:125`；空 prompt/附件校验 `main.go:1743-1755`。
4. 解析模型/联网/会话/账号元数据 `main.go:1756-1766`；`registry.Resolve` 解析模型别名 `main.go:1762`。
5. 续聊匹配 `main.go:1792`（带 tools 的请求**不做跨会话续聊**，每请求新 thread）。
6. 工具桥注入 `main.go:1818-1827`（首轮 few-shot / 续轮 summary+结果）。
7. `startConversationTurn` `main.go:1830`，设置 `X-Conversation-Id`。
8. 分流：
   - `stream=true` → `writeChatCompletionLiveStream` `main.go:2288`（§Q3）；
   - 否则 `runPrompt` `main.go:1841` → account pool → Provider。
9. 非流式：`extractToolCalls` → 可选 `synthesizeToolCall/synthesizeTaskCall` → `filterCallsToAvailable` → `normalizeToolArgumentsWithSchema` → `unmaskToolCallPathsForWorkingDirectory` `main.go:1856-1877`。
10. `buildChatCompletionWithToolsForWorkingDirectory` `openai.go:1170`，`writeJSON`。

### Q2. `/v1/responses` 链路是什么？

`handleResponses` `main.go:2000`：

1. `decodeResponsesRequestBodyFromRaw` `main.go:1647`，但 **handler 把 raw payload 丢弃**（`typed, _, err := ...` `main.go:2006`）。
2. `previous_response_id` 查 `ResponseStore` `main.go:2017-2023`。
3. `normalizeResponsesInputFromPartsWithInstructionsAndToolResults` `openai.go:258`（`instructions` 进入 HiddenPrompt `openai.go:288-290`）。
4. 续聊匹配 `main.go:2072`；fresh-thread 时用 `buildFreshThreadReplayPromptFromStoredResponse` `main.go:2104-2106`。
5. 工具桥注入 `main.go:2093-2103`。
6. stream → `writeResponsesLiveStream` `main.go:2618`；非 stream → `buildResponsesOutputWithCalls` `openai.go:1361` + `saveResponseWithAccount` `main.go:2155`。

### Q3. `stream=true` 的完整生命周期是什么？

```
handler
  → writeChatCompletionLiveStream             main.go:2288
      var streamSieve = toolStreamSieve{}     main.go:2299   // 抑制半截工具标记
      startStream() (首次角色帧 + SSE header)  main.go:2317-2329
      1.5s 主动 flush 定时器                    main.go:2386-2409
  → runPromptStreamWithSink                   main.go:1706
  → runPromptWithAccountPoolWithSink          request_dispatch.go:708
      ctx = context.WithTimeout(r.Context(), streamRequestTimeout)  :714
  → runPromptWithSessionWithSink              account_pool.go:310
  → client.RunPromptStreamWithSink            notion_client.go:4396
  → streamRunInferenceTranscript              notion_client.go:3966
  → runInferenceTranscriptHTTP (未 ReadAll)   notion_client.go:1336
  → consumeNDJSONStreamWithIdleClose          notion_client.go:2804
      每行 → handleLine → mergeAgentInferenceEvent / applyPatchOperation
      → emitFullText → sink.EmitText(增量后缀) notion_client.go:2318
  → main.go:2410 InferenceStreamSink.Text → emitContent
      → toolStreamSieve.feed → safeWriteData (SSE + Flush)  main.go:2968
  → 流结束后 extractToolCalls / synthesize     main.go:2476-2497
  → 有 tool_calls → 首片→增量片→finish(tool_calls)→usage→[DONE]  main.go:2511-2559
  → 否则 → finish(stop)→usage→[DONE]          main.go:2605-2615
```

**结论**：Chat 路径**是真流式**（上游 NDJSON 逐行、suffix delta、flush）。Responses 路径同理但带 `sequence_number`。Anthropic 路径经 `io.Pipe` + converter 实时转换 `anthropic.go:149-192`。

**但存在"观感非流式"的现实因素**（详见 Q15）：默认开 web search、续聊多次往返、上游本身 TTFB 高、某些情况整段缓冲。

### Q4. Notion upstream HTTP client 在哪里创建？

`newNotionAIClient` / `newNotionAIStreamingClient` `notion_client.go:828-834` → `newNotionAIClientWithMode` `notion_client.go:921-945`：

```go
transport := cachedNotionHTTPTransport(normalizedCfg, accountEmail, resolver, upstream) // :925
...
HTTPClient: &http.Client{ Timeout: clientTimeout, Transport: transport },              // :940
```

**每个请求新建 `*http.Client` 对象，但底层 `*http.Transport` 来自全局缓存。**

### Q5. 是否每个请求都创建新的 HTTP/TLS connection？

**稳态下否。** `cachedNotionHTTPTransport` `notion_client.go:858-919` 按 `(base/origin/proxy/resin/AccountEmailKey)` 缓存 transport，配置了连接池/keep-alive：

```go
transport := &http.Transport{
    DialContext:           (&net.Dialer{Timeout: 10s, KeepAlive: 30s}).DialContext,  // notion_client.go:877
    TLSHandshakeTimeout:   10 * time.Second,
    ResponseHeaderTimeout: 45 * time.Second,   // 仅等 header，不是整体推理超时
    IdleConnTimeout:       90 * time.Second,
    MaxIdleConns:          64,
    MaxIdleConnsPerHost:   16,
    ...
}
```

缓存上限 64，逐出时 `CloseIdleConnections()` `notion_client.go:858-919`。

**例外**：
- browser/surf 回退 `runInferenceTranscriptInBrowserWithSurf` **每次新建** impersonated client（仅 `trust-rule-denied` 触发 `notion_client.go:1379-1389`）。
- 每请求 `&http.Client{}` 对象分配本身是浪费，但非 TLS 冷启动。
- transport 缓存 miss（账号×代理组合首次）会冷启动。

### Q6. 是否存在重复认证、重复 workspace 查询、重复 conversation 初始化？

**有，主要是续聊回合。**

- 健康探测 `getInferenceTranscriptsForUser`：**有 45s/账号 缓存** `request_dispatch.go:307-316`（`config.go:514 ProbeCacheTTLSeconds=45`）。
- `ensureSessionLiveMetadata` 每请求调用 `notion_client.go:4220`，但元数据完整时 `probeMetadataNeedsBackfill()==false` → **0 网络** `notion_client.go:767-769`。
- **续聊回合额外 +3 次上游往返**（`preparePromptRequest` `notion_client.go:4201-4219`）：
  - `prepareContinuationDraftFromThread` = `syncThread` + `syncThreadMessages`（+2）
  - `saveContinuationScaffold` = `saveTransactionsFanout`（+1）
  - 答后 `markInferenceTranscriptSeen`（+1）`notion_client.go:4369-4371, 4450-4452`
  - 若流无终态 agent，`loadFinalAnswerOnce`/`pollFinalAnswer` 再 +2
- **每个请求都写 config 文件**：`MutateAccount` → `SaveAndApply` → `saveConfigFile` `main.go:552-558`（账号计数器每请求变动导致 `persistedConfigEqual` 不等）。
- 认证刷新：仅 900s 定时 `session_refresh.go:346-382` + 认证错误时，非每请求。

### Q7. conversation/session 是否真正复用？

- **上游 thread：默认复用**（`force_fresh_thread_per_request=false` `config.go:525`），thread id 落 SQLite `conversation_session.go:16-33`。
  - ⚠️ 但 `config.docker.json` 把 `force_fresh_thread_per_request` 设为 `true` → 等于每请求重放全历史。
- **服务端 `SessionInfo`：无内存缓存**，每请求从 `probe.json` 重读 `request_dispatch.go:348-357`；cookie 刷新 900s/错误时。
- **HTTP 连接：复用**（Q5）。

结论：**"session 复用"目前只等于 thread id 复用 + transport 复用，缺少 provider session 对象生命周期管理。**

### Q8. account pool 如何工作？

`buildDispatchCandidateOrder` `account_pool.go:202-212` 过滤 `accountDispatchEligible`（未禁用、有 artifacts、非冷却）→ `sortDispatchCandidates` `account_pool.go:164-200`（active 优先、Priority、quota、失败次数、LRU、email）。
- pinned / `AllowPinnedAccountFallback` `request_dispatch.go:186-220`。
- 全员冷却 → `buildHalfOpenCandidates` 半开（最早冷却者优先）`request_dispatch.go:29-47, 517-523`。
- 每候选：`TryAcquireAccountDispatchSlot` → `markAccountDispatchStart` → `loadReadyDispatchSession` → 执行；成功/失败 `MutateAccount`。
- 每账号独立 transport（缓存键含 `AccountEmailKey`）。
- active 切换按 `shouldPersistDispatchedAccountAsActive` 决定并落盘 `request_dispatch.go:574-581`。

### Q9. upstream error 如何处理？

类型：
- `inferenceStepError{SubType,TraceID,Retryable}` `notion_client.go:480-488`
- `notionAPIError{StatusCode,Message}` `notion_client.go:541-556`
- `errAccountStarved` `notion_client.go:522-524`；`errDispatchCapacityExceeded`/`errNoEligibleAccounts` `request_dispatch.go:23,27`

分类：quota-exhausted（`workspace_rotation.go:66-75`）、temporarily-unavailable（`:78-87`）、`IsRotationWorthyError`（`:91-99`）、session-retryable（`session_refresh.go:21-47`）、trust-rule-denied（`notion_client.go:1325-1334`）。

**缺口（P0）**：`premium-feature-unavailable` / `insufficient_quota` / HTTP `402` / 推理 `429` **完全没有分类**（全仓 grep 0 命中），会落成 502 `upstream_error`。

`writeUpstreamError` `main.go:2163-2175` 仅 3 分支；超时判定是字符串 `strings.Contains(lower,"timeout")` 而非 `errors.Is(context.DeadlineExceeded)`。

### Q10. stream 中途失败如何处理？

- **Chat**：首字节后失败 → 发 `error` SSE（`code=upstream_aborted`）再 `[DONE]`，**不映射 stop** `main.go:2445-2455` ✅
- **Responses**：首字节后失败 → **伪造成 `response.completed`** `main.go:2765-2809` ❌（客户端以为成功）
- 首字节前 → `writeUpstreamError`。
- 顶层 panic recovery：`main.go:3089-3118`（SSE 已开始则发 `event: error` + DONE）。

### Q11. tool_bridge 当前到底完成了什么？

现有能力：注入（Prompt Adapter）+ few-shot（"写技术文档"框架 `tool_bridge.go:543`）+ 提取（XML 标签 / markdown fence / 裸 JSON `tool_bridge.go:658-832`）+ 过滤 `filterCallsToAvailable` `:330` + schema 归一 `normalizeToolArgumentsWithSchema` `:381` + 路径 mask/unmask + 合成 `synthesizeToolCall`/`synthesizeTaskCall` `:881/:945` + `toolStreamSieve` 流式抑制 `:1336`。

**关键缺陷**：
1. **Chat 续聊完全丢失 assistant `tool_calls`**：`extractChatConversationPromptSegment` 只读 `content`，忽略 `message["tool_calls"]` `openai.go:177-202`。
2. `buildToolResultsPrompt` 不带 `call_id/name/arguments`，只塞匿名 `<<<DATA>>>` `tool_bridge.go:849-873`。
3. 续聊 `buildToolBridgeSummary` **丢掉定义输出格式的 assistant few-shot** `tool_bridge.go:1738-1767`。
4. 合成器会填 `"TODO"`/空串/伪造脚本 `tool_bridge.go:1194-1224, 1864-1889`。
5. `buildToolBridgePrompt` 工具清单**重复列两遍**（`rendered` 两轮 append）`tool_bridge.go:525-562`。
6. `extractToolCalls` 宽松到把正文 `{"name":...}` 当调用（误报）。
7. `toolSieveMaxBlockAge`/`lastFeed` 是**死代码**，超时闸从未生效 `tool_bridge.go:1332,1342`。
8. 合成默认关闭（`config.Features.AllowTextToolSynthesis` 默认 false `config.go:521-533`）→ 上游不吐 exact block 时 **0 工具**。

### Q12. 当前是否已经存在 parser / event abstraction？

**几乎没有。**
- 唯一抽象：`InferenceStreamSink{Text,Reasoning,ReasoningWarmup,KeepAlive}` `stream_sink.go:3-8`。
- **无规范化事件类型**（无 `TextDelta/ToolCallStart/ToolCallDelta/ToolCallEnd/Usage/Error/Done`）。
- 协议 writer 直接构造 `map[string]any`（`openai.go:1498-1550` 等）。
- **工具调用只在流结束后解析** `main.go:2476`，无法增量。
- 内容过滤是字符串级：`sanitizeAssistantVisibleText` `notion_client.go:153`、`sanitize_ledger.go`、`unknown_marker.go`。

### Q13. 当前代码中哪些模块可以直接保留？

- `cachedNotionHTTPTransport` + keep-alive 配置 `notion_client.go:858-919`
- 健康探测缓存 `request_dispatch.go:51-65`
- 账号池 + 冷却 + 半开 `account_pool.go`、`request_dispatch.go`
- ConversationStore + SQLite 持久化 `conversations.go`、`sqlite_store.go`
- ResponseStore + TTL `response_store.go`
- NDJSON 解析器 + 静默/空闲看门狗 `notion_client.go:2804+`
- `sanitize` 账本 `sanitize_ledger.go`、`unknown_marker.go`
- Admin WebUI / 静态服务 `admin.go`
- ModelRegistry `models.go`
- auth / API key `main.go`
- metrics 骨架 `metrics.go`
- Anthropic converter `anthropic_stream.go`
- `splitUTF8Chunks` `openai.go:1464`
- `toolChoiceNone`/`toolsAllowedByChoice` 硬边界 `tool_bridge.go:309/323`

### Q14. 哪些模块存在架构债务？

- **死代码**：`runPromptWithPromptGuard` 及整条认知重构链 `prompt_guard.go:427`；`writeChatCompletionStream` `main.go:2195`；legacy `unmaskPathArgs`/`unmaskToolCallPaths` `tool_bridge.go:202-221`；browser helper stubs；`toolSieveMaxBlockAge`。
- **巨型文件**：`notion_client.go`(4470)、`main.go`(3253)、`tool_bridge.go`(1890)——上游/协议/业务耦合。
- **无事件模型**；协议 writer 与 raw map 耦合。
- **每请求写 config** `main.go:552-558`。
- **无 `SessionInfo` 内存缓存** `request_dispatch.go:348-357`。
- **静默丢弃生成参数**（Q16/§4）。
- **续聊 +3 往返**（Q6）。
- **Responses 中断伪造成功**（Q10）。

### Q15. "聊天慢"的最可能 TOP 5 原因？

> 以下为静态推断，需 Phase 2 用 instrumentation 证实。

1. **`use_web_search=true`（`config.docker.json` / `config.go:522`）+ 高 thinking 档**——上游每次联网，首 token 被拉长。
2. **续聊 +3 次上游往返 + 答后 +1**（Q6）。
3. **每请求 config 落盘 + 每请求 `&http.Client{}` 分配 + 探测缓存 miss**（Q6/Q5）。
4. **上游本身首 token ~2.9s**（`STATUS.md` 记录 TTFB p50 ~2.9s），且可能只在最终 `record-map` 一次性给全文 `notion_client.go:2354`。
5. **`force_fresh_thread_per_request=true`（docker 默认）时整段历史重放** `main.go:1799`。

### Q16. 哪些问题会直接导致"不能作为 API 使用"？

- **静默丢弃生成参数**：Chat 缺 `temperature/top_p/max_tokens/max_completion_tokens/response_format/n/presence_penalty/frequency_penalty/seed/user/parallel_tool_calls/reasoning_effort`（`openai_types.go:9-34`）；Responses 缺 `max_output_tokens/temperature/top_p/reasoning/text/parallel_tool_calls`（`:36-54`）；Anthropic 的 `max_tokens/temperature` 设进 internalBody 后又被 Chat typed 解码丢弃（`anthropic.go:90-94`）。
- **`finish_reason=length` 不可达**（无 max token 执行）`openai.go:1178,1195`。
- **Tool Calling 不可靠 + 多轮丢上下文**（Q11）。
- **Responses 中断伪造成功**（Q10）。
- **402/额度错误分类缺失**（Q9）。
- **身份顺应 / Prompt Adapter 是死代码**（`prompt_guard.go` 无调用点）。
- **路径还原回退服务端 home** `tool_bridge.go:115,232`（Codex 不写 `Primary working directory:`）。
- **无 `request_id` / TTFB / TTFT 指标**（Q类 observability）。
- **零真实 integration test**（Q18）。

### Q17. 当前测试覆盖了哪些情况？

全仓仅 4 个 `_test.go`（均在 `internal/app`），全部用进程内 `ServeHTTP` + `runPromptOverride` mock，**无 `httptest.NewServer` 模拟上游**：

| 文件 | 覆盖 |
|---|---|
| `audit_regression_test.go`(579) | raw 保真、`tool_choice=none` 边界、UTF-8 分片、路径 round-trip、admin key、Responses 多轮 + 事件序列 |
| `stability_regression_test.go`(636) | 半开、pinned fallback、NDJSON 看门狗、Anthropic converter、冷却、`MutateAccount` 并发、metrics、space pool |
| `response_persistence_regression_test.go`(111) | SQLite 重启回放、TTL |
| `register_live_probe_test.go`(35) | 需 `N2A_LIVE_TEST` 的手动 live 探测 |

钩子 `testHookTryRefreshAccount`/`testHookSaveAndApply` `session_refresh.go:13-14` **从未被测试设置** → session refresh 路径未测。

### Q18. 缺少哪些关键 integration tests？

明确**缺失**：`upstream timeout`、`account failover`、`session expiration`、`client disconnect`。
`upstream EOF` 与 `multi-turn` 仅单元/局部。**没有任何一项是对模拟上游的端到端测试**。

---

## 3. P0 / P1 / P2 优先级

> P0 定义：不修就不能称为可用的 OpenAI 兼容 API。

### P0
1. **生成参数透传或显式报错**（Chat/Responses/Anthropic）；`max_tokens` 可触发 `finish_reason=length`。
2. **Tool Call 协议闭环**：Chat 续轮回填 assistant `tool_calls`（name/arguments/call_id）+ tool 结果；保留格式契约；启用**有界**结构化合成。
3. **Prompt Adapter Strategy 落地**（替代 `prompt_guard.go` 死代码）：Native / JsonAction / TaggedXml，由 Provider 选择。
4. **错误分类补全**：`premium-feature-unavailable`/`insufficient_quota`/402/429/session/5xx 正确映射；超时用 `errors.Is`。
5. **Responses 首字节后失败禁止伪造 `response.completed`**。

### P1
6. 真·增量 `tool_calls` 事件（不再只流结束后解析）。
7. Session 复用：`SessionInfo` 内存缓存、合并续聊 +3 往返、避免每请求写 config。
8. `request_id` + TTFB/TTFT/总延迟 instrumentation。
9. 低延迟开关（**不改默认**，只提供开关）。
10. 连接复用验证 + 每请求 `&http.Client{}` 收敛。

### P2
11. 规范化事件模型（Provider Event → Normalized Event → Protocol Renderer）。
12. Provider 接口抽象（为未来 Claude/Cursor/ChatGPT Provider 留位，本期不实现）。
13. Anthropic usage/cache token、`stop_sequence`；`/v1/models` 别名与 `created`。
14. 管理页指标（成功率、P95 TTFT、busy session）。
15. `ARCHITECTURE.md` / `BENCHMARK.md` / `TEST_REPORT.md` / README。

---

## 4. 目标架构映射（保留 / 补强 / 新增）

```
Client
  ▼
API Gateway          [保留 main.go ServeHTTP + 路由]
  ▼
Request Dispatcher   [保留 request_dispatch.go；补强错误分类/预算]
  ▼
Agent Runtime        [补强]
  ├── Session Manager      [补强：SessionInfo 缓存 + acquire/release]
  ├── Stream Processor     [新增：Normalized Event Model]
  ├── Tool Protocol        [补强：完整 tool call 闭环 + Prompt Adapter Strategy]
  ├── Retry / Recovery     [补强：safe/unsafe retry 区分]
  ├── Account / Provider Selection  [保留 account_pool.go]
  └── Observability        [新增：request_id + TTFB/TTFT]
  ▼
Provider             [新增接口；Notion 作为首个实现]
  ▼
Notion Web           [保留 notion_client.go 作为 NotionProvider 内核]
```

**原则**：不重写 `notion_client.go` 的 wire 解析；在其外抽 Provider 接口；协议渲染与上游事件解耦。

---

## 5. 风险与不做清单

### 风险
- 上游 Notion 行为漂移（`STATUS.md` 已记录多次：模型拒答/格式漂移/配额错变体）——必须有回归样本与看门狗。
- 账号配额与注册号源不稳定，会表现为"API 不稳"，需与代码缺陷区分（observability）。
- 续聊往返与 config 落盘优化若做错会改变语义，必须锁死 `file:line` 回归。

### 明确不做
1. 不重写 Notion provider；2. 不微服务化；3. 不引入 Redis/Kafka；4. 不加注册/营销/计费；5. 不把 jailbreak 当核心；6. 不把 scraper 与 API 层写成一个巨型文件；7. 不为测试删功能；8. 不改 API contract；9. 无 benchmark 不宣称提速；10. 无测试不宣称稳定。
11. **本期不实现服务端任意工具执行平台**（只做标准 `tool_calls` 产出 + 续轮，预留 Executor 接口）。
12. **不把 Prompt Adapter 做成对某厂商安全策略的绕过**——它是"结构化输出协议适配"，用于上游无原生 function calling 时获得可解析 tool intent。

---

## 6. 下一阶段（Phase 1，待批准后开工）

范围锁定 **OpenAI Python SDK + `/v1/chat/completions`**：
1. 生成参数透传/显式报错（P0-1）
2. Tool Call 续轮闭环（P0-2）
3. Prompt Adapter Strategy 落地（P0-3）
4. 错误分类补全（P0-4）
5. OpenAI Python SDK 冒烟 + 全量测试回归

> 每个 Phase 收尾：`go test ./...` 全绿 → 现有功能回归 → 记录改动 → 给性能变化 → 才进下一阶段。
