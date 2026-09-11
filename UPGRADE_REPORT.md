# UPGRADE REPORT — Web Agent → OpenAI Compatible API

> 范围：Phase 0–3 完整交付，Phase 7–8 关键部分交付。Phase 4/5/6 部分/延后（附理由）。
> 原则遵守：不推倒重写、复用现有模块、无 benchmark 不宣称提速、无测试不宣称稳定。

## 已交付

| Phase | 内容 | 产物 |
|---|---|---|
| 0 架构审计 | 18 问 + P0/P1/P2 | `ARCHITECTURE_REVIEW.md` |
| 1 API 基础兼容 | 生成参数兑现/上报、tool 续轮闭环、Prompt Adapter 落地、错误分类 | `PHASE1_REPORT.md` + 5 测试文件 |
| 2 低延迟 & Streaming | request_id + TTFB/TTFT instrumentation、TTFT metric、flush 开关 | `PHASE2_REPORT.md` + `timing_test.go` |
| 3 Session Runtime | SessionInfo 缓存（mtime+签名+TTL）、config 落盘核实 | `PHASE3_REPORT.md` + `session_cache_test.go` |
| 7 测试 | timeout/EOF/断连/session 过期/错误 schema 集成测试 | `integration_regression_test.go` + `TEST_REPORT.md` |
| 8 文档 | 架构、README、benchmark 方法、一键测试 | `ARCHITECTURE.md`、`BENCHMARK.md`、README、`Makefile`、`scripts/test.ps1` |

## 默认行为影响

- **未改动任何现有默认值**（`use_web_search` 等保持原样）。
- `prompt.profile` 默认仍为 `cognitive_reframing`，但其注入链路此前是死代码，现已真正生效（P0 修复）。
- 新增开关 `streaming.initial_flush_delay_ms`（缺省=历史 1500ms）。

## 明确延后（附理由）

| 项 | 原因 | 依赖 |
|---|---|---|
| Phase 4 Normalized Event Model | 大规模重构，风险高；现协议 writer 稳定；无明确用户可见收益前不重写 | 需完整回归 + 时间窗 |
| Phase 5 真·增量 tool_calls 事件 | 当前流结束后成对下发对主流客户端可用；增量解析工具块需更强 fuzz 防泄漏 | 工具桥 sieve 扩展 |
| Phase 6 三预算拆分 / 账号端到端 failover 测试 | 需可注入 Provider/transport mock；60s 全局 clamp 已移除 | Provider 接口抽象 |
| Responses 中断不伪造成功 | 属 Responses 链路（本期只保证不回归） | Phase 4 |
| 续聊 +3 往返削减 | 影响续聊语义正确性，无 live 回归不擅动 | live 验证 |

## 测试状态

`go vet ./... && go build ./... && go test ./... -count=1` 全绿；含 OpenAI Python SDK 真冒烟（models/非流式/流式/tool_calls）。详见 `TEST_REPORT.md`。

## 已知债务（继承自审计）

- Provider 未抽象为接口（`NotionAIClient` 直接耦合）。
- Responses 首字节后失败仍可能伪造 `response.completed`（本期未动）。
- Anthropic `max_tokens/temperature` 仍被下游 Chat typed 丢弃（本期只做 chat）。
- 无 cwd 时路径回退服务端 home。

以上均已记录在 `ARCHITECTURE_REVIEW.md`，作为下一轮输入。
