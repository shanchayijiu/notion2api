# PHASE 1 REPORT — API 基础兼容（OpenAI Chat Completions）

> 范围锁定（用户拍板）：**OpenAI Python SDK + `/v1/chat/completions`**。
> Responses / Anthropic 本期只保证不回归。
> 基线：`main` @ `4c3e3d7` + Phase 0 文档。

## 1. 交付内容（P0）

### P0-1 生成参数：承接、兑现或显式上报
- `internal/app/openai_types.go`：`chatCompletionsRequestBody` 新增 `temperature/top_p/max_tokens/max_completion_tokens/n/response_format/parallel_tool_calls/presence_penalty/frequency_penalty/seed/reasoning_effort/logprobs/top_logprobs/user`，typed 解码不再静默丢弃；fallback 提取路径同步解析。
- `internal/app/generation_params.go`（新增）：
  - `max_tokens` / `max_completion_tokens` → **本地强制截断**，`finish_reason=length`（非流式 + 流式）。
  - `n>1` → **400 `invalid_request_error`**（明确报不支持，而非悄悄只回一个）。
  - `temperature/top_p` 越界 → 400；合法值接受并在 `X-Notion2API-Unsupported-Params` 响应头 + 日志中**显式上报**未转发。
  - `response_format`：`json_object` / `json_schema` → 注入 instructions 指令；未知类型 → 400。
  - `parallel_tool_calls=false` → 只保留第一个 tool call。
- 关联改动：`InferenceResult.Truncated`、`PromptRunRequest.MaxOutputTokens/ParallelToolCalls`（`notion_client.go`）；`openai.go` finish_reason 支持 `length`；`main.go` 非流式/流式接入。

### P0-2 Tool Call 续轮闭环
- `internal/app/tool_bridge.go` 新增 `buildToolExchangePrompt`：续轮时把 **assistant `tool_calls`（name/arguments）+ `[call_id]` + 工具结果**成对回填上游，Chat 路径按 `tool_call_id` 配对，Responses 路径按 `call_id` 配对。
- `internal/app/main.go`：Chat 与 Responses 续轮改用 `buildToolExchangePrompt`（回退到旧 `buildToolResultsPrompt`）。
- 效果：模型下一轮能看到"自己调用过什么"，不再失忆。

### P0-3 Prompt Adapter Strategy 落地（替换死代码）
- `internal/app/prompt_adapter.go`（新增）：`promptAdapter{name,prefix}` + `native/custom/cognitive_reframing/toolbox_capability_expansion` 策略选择，注入 **instructions 通道**（不插独立 assistant 轮，避免 parrot），幂等。
- `internal/app/main.go`：`runPrompt` / `runPromptStream` / `runPromptStreamWithSink` 统一入口应用（覆盖 chat/responses/anthropic/SillyTavern/账号池/无账号回退）。
- 配置 `prompt.profile=none` 时保持 native 不注入。
- 说明：这是**结构化输出/任务框定**策略，不是安全策略绕过。

### P0-4 错误分类
- `internal/app/upstream_errors.go`（新增）：`classifyUpstreamError` 把 upstream 失败映射为 OpenAI 语义：
  - quota / `premium-feature-unavailable` / `insufficient_quota` / 402 → `429 insufficient_quota`
  - 429 → `429 rate_limit_error`
  - 401/403 / session → `401 invalid_api_key`
  - `temporarily-unavailable` / 5xx → `503 upstream_unavailable`
  - timeout/deadline → `504 upstream_timeout`
  - 其余 → `502 upstream_error`
- `main.go writeUpstreamError` 改用分类器（保留 `dispatch_capacity_exceeded` 优先分支）。

## 2. 测试

| 文件 | 内容 |
|---|---|
| `internal/app/generation_params_test.go` | max_tokens 截断+length、n>1 400、temperature 越界 400、unsupported 头上报、response_format 指令、parallel_tool_calls、流式 length |
| `internal/app/upstream_errors_test.go` | 分类表单测 + quota→429/insufficient_quota HTTP 断言 |
| `internal/app/tool_exchange_test.go` | exchange 配对（Chat/Responses）+ 两轮 Chat 工具续轮回填断言 |
| `internal/app/prompt_adapter_test.go` | 注入、禁用 native、幂等 |
| `internal/app/sdk_compatibility_test.go` | **OpenAI Python SDK 真冒烟**（models / 非流式 / 流式 / tool_calls），无 Python 时自动 skip |

## 3. 验证结果

```
go build ./...            # PASS
go vet ./...              # PASS
go test ./... -count=1    # PASS（含 4.5s 的 Python SDK 兼容测试）
```

- Python SDK 冒烟实测通过：`MODELS_OK / NONSTREAM_OK / STREAM_OK / TOOLS_OK / ALL_OK`（openai 2.47.0）。
- 既有回归（audit/stability/persistence）全绿，未删功能。

## 4. 性能

本阶段**未做 benchmark**，不声明任何性能数字（遵守"无 benchmark 不宣称提速"）。
首 token 延迟 / session 复用优化属 Phase 2/3。

## 5. 本阶段未做（按计划后移）

- 生成参数中 `temperature/top_p` 等的上游真实生效（Notion 无对应控制）→ 仅显式上报。
- 真·增量 `tool_calls` 流式事件（当前仍在流结束后提取）→ Phase 4/5。
- 拒绝重试 / 会话恢复 / failover → Phase 6。
- Responses 中断伪造成功修复、Anthropic 参数透传 → 后续 Phase（本期不回归即可）。

## 6. 下一步建议

Phase 2（低延迟 & Streaming）：先加 timing instrumentation（request_received / first_upstream_byte / first_token / last_token），测量后再动，产出 `BENCHMARK.md`。
