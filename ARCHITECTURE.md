# ARCHITECTURE

Notion2API 把带 Web 壳的 Notion AI 暴露为 OpenAI 兼容 API（`/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/models`）。
本文描述目标架构与当前实现的对应关系。

## 分层

```
Client (OpenAI SDK / Claude Code / Codex / Cline / OpenWebUI)
  │
  ▼
API Gateway            internal/app/main.go (ServeHTTP + 路由)
  │  request_id / X-Request-Id、CORS、鉴权、body 限制
  ▼
Request Dispatcher     internal/app/request_dispatch.go
  │  账号选择、健康探测缓存、预算、半开冷却、client disconnect 传播
  ▼
Agent Runtime
  ├── Session Manager        session_cache.go / session_refresh.go
  ├── Stream Processor       main.go live stream writers + stream_sink.go
  ├── Tool Protocol          tool_bridge.go
  ├── Prompt Adapter         prompt_adapter.go
  ├── Retry / Recovery       account_pool.go / workspace_rotation.go
  └── Observability          timing.go / metrics.go
  ▼
Provider
  │  NotionAIClient (notion_client.go) + transport 缓存 + surf 回退
  ▼
Notion Web (https://www.notion.so)
```

Provider 与 Runtime 的边界目前是 `NotionAIClient`（`RunPrompt` / `RunPromptStreamWithSink`）。新增 Claude Web / ChatGPT Web 等 Provider 时，实现同一组入口即可复用 Dispatcher/Runtime。

## 请求链路（chat completions）

1. `ServeHTTP` 生成 request id、设置 `X-Request-Id`、启动 timing。
2. `handleChatCompletions`：typed+raw 解码（未知字段不丢）→ 附件校验 → `normalizeChatInputFromParts`。
3. 生成参数 `generation_params.go`：`max_tokens` 本地兑现、`n>1`/越界参数明确 400、不支持的采样参数走 `X-Notion2API-Unsupported-Params` 显式上报、`response_format` 注入指令。
4. 续聊匹配 → 工具桥注入（首轮 few-shot / 续轮 `buildToolExchangePrompt`）。
5. `prompt_adapter.go` 在 `runPrompt*` 入口注入所选策略（默认 `cognitive_reframing`，`profile=none` 关闭）。
6. 流式 → `writeChatCompletionLiveStream`；非流式 → `runPrompt`。
7. `runPromptWithAccountPool` → 候选排序 / 健康探测（45s 缓存）/ 半开 → `runPromptWithSession`。
8. `NotionAIClient.preparePromptRequest` → `buildInferencePayload` → NDJSON 流解析。
9. 结果 → 工具提取/合成 → 协议渲染（OpenAI / Anthropic）→ `[DONE]` / `usage`。

## 正常化与协议

- 请求模型：`openai_types.go`（Chat/Responses typed 结构，保留未知字段）。
- 输出渲染：`openai.go`（chunk/response builder）、`anthropic_stream.go`（OpenAI SSE → Anthropic 事件）。
- 内容净化：`sanitize_ledger.go`（码点守恒账本）、`unknown_marker.go`（未登记标记扫描）。
- 说明：当前**没有独立的 Normalized Event Model**（Phase 4 目标）；上游事件经 `InferenceStreamSink`（4 回调）直接驱动协议 writer。工具调用在流结束时提取后分片下发。

## Tool Protocol

- 注入：`buildToolBridgePrompt`（首轮完整 few-shot）/ `buildToolBridgeSummary`（续轮摘要）。
- 提取：`extractToolCalls`（`<tool_call>` / ```json / 裸 JSON）。
- 回填：`buildToolExchangePrompt` 把 assistant `tool_calls`(name/arguments/call_id) 与工具结果成对送回上游。
- 约束：`tool_choice=none` 为硬边界（不注入/不提取/不合成/不 mask）；`tool_choice=required`/指定工具 强制合成；`parallel_tool_calls=false` 只保留首个。
- 路径：优先客户端工作目录（`Primary working directory:`），不可逆时用 `~/__client_path__/...` 标记；无 cwd 时保留原文/服务端 home 回退（已知债务）。

## 错误模型

`upstream_errors.go` 把上游失败映射到 OpenAI 语义：
`insufficient_quota`(429) / `rate_limit_error`(429) / `invalid_api_key`(401) / `upstream_unavailable`(503) / `upstream_timeout`(504) / `upstream_error`(502)。

流式中断：首字节前用 HTTP 状态 + JSON error；首字节后发 `error` 事件（`code=upstream_aborted`）再 `[DONE]`，不映射 `stop`。

## 可观测性

- `timing.go`：每请求 `request_received → first_token → last_token → completed`，输出 `[req]` 日志（含 `ttft_ms/total_ms/retries/stream_error`）。
- `metrics.go`：`/metrics` 暴露 `request_duration_seconds`、`ttft_seconds`、`transport_call_duration_seconds`、`dispatch_slot_inflight`、`sqlite_op_duration_seconds` 等。

## 配置开关（延迟相关，默认不变）

- `features.use_web_search`：是否联网检索（默认沿用配置值）。
- `streaming.initial_flush_delay_ms`：SSE 首字节主动 flush 延时；缺省 1500ms，设 0 为最低延迟。

## 与验收标准 v4 的关系

本架构在现有实现上补强，不做推倒重写；Phase 4（Normalized Event Model）与 Phase 5（增量 tool_calls）为后续项。
