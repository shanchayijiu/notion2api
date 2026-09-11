# TEST REPORT

> 生成于 Phase 1–3 收尾。命令：`go vet ./... && go build ./... && go test ./... -count=1`（等价 `make test` / `scripts/test.ps1`）。

## 结果

```
go vet ./...              PASS
go build ./...            PASS
go test ./... -count=1    PASS
```

`internal/app` 单测（含进程内 HTTP 集成 + OpenAI Python SDK 真冒烟）全绿；`cmd/*` 无测试文件。

## 测试清单

### 既有回归
| 文件 | 覆盖 |
|---|---|
| `audit_regression_test.go` | raw 保真、`tool_choice=none` 三协议边界、UTF-8 分片、路径 round-trip、admin key、Responses 多轮与事件序列 |
| `stability_regression_test.go` | 半开、pinned fallback、NDJSON 看门狗、Anthropic converter、冷却、MutateAccount 并发、metrics、space pool |
| `response_persistence_regression_test.go` | SQLite 重启回放、TTL |
| `register_live_probe_test.go` | 需 `N2A_LIVE_TEST` 的 live 探测 |

### Phase 1 新增
| 文件 | 覆盖 |
|---|---|
| `generation_params_test.go` | max_tokens 截断+length、n>1 400、温度越界 400、unsupported 头上报、response_format、parallel_tool_calls、流式 length |
| `upstream_errors_test.go` | 错误分类表 + quota→429 HTTP |
| `tool_exchange_test.go` | 工具交换配对（Chat/Responses）+ 两轮续轮回填 |
| `prompt_adapter_test.go` | 策略注入、native 关闭、幂等 |
| `sdk_compatibility_test.go` | OpenAI Python SDK：models/非流式/流式/tool_calls |

### Phase 2 新增
| 文件 | 覆盖 |
|---|---|
| `timing_test.go` | X-Request-Id 回显/生成、MarkToken 一次、TTFT metric、flush delay 开关 |

### Phase 3 新增
| 文件 | 覆盖 |
|---|---|
| `session_cache_test.go` | 缓存命中+深拷贝、probe 失效、身份签名失效、config 落盘等值判定 |

### Phase 7 新增（集成）
| 文件 | 覆盖 |
|---|---|
| `integration_regression_test.go` | upstream timeout→504、session expired→401、流中 EOF→`upstream_aborted`+DONE、pre-header 错误用 HTTP 状态、**client disconnect 取消上游**、错误 schema 形状 |

## 覆盖矩阵（对照目标 10 项）

| # | 场景 | 状态 |
|---|---|---|
| 1 | non-stream chat | ✅ HTTP 集成 |
| 2 | stream chat | ✅ HTTP 集成 |
| 3 | multi-turn | ✅ HTTP（chat 工具续轮 + responses 续轮） |
| 4 | tool call | ✅ HTTP + Python SDK |
| 5 | tool result continuation | ✅ HTTP |
| 6 | upstream timeout | ✅ HTTP（504） |
| 7 | upstream EOF | ✅ HTTP（流中 EOF → upstream_aborted） |
| 8 | account failover | ⚠️ 单元级（候选排序/半开/冷却/rotation pass-through）；端到端需 mock 网络，暂缺 |
| 9 | session expiration | ✅ HTTP（401）+ session cache 失效测试 |
| 10 | client disconnect | ✅ HTTP（取消传播） |

## 尚未覆盖 / 后续

- account failover 的**端到端**（需要可注入的 Provider/transport mock；Phase 4/6 补）。
- 真·增量 tool_calls 事件、Responses 中断不伪造成功（Phase 4/5）。
- live `BENCHMARK.md` 数据（需健康账号实例）。

## 已知环境说明

本机 Windows `time.Now()` 分辨率较粗，亚毫秒 TTFT 可能为 0；已用 `requestTimer.HasToken()` 保证仍记录观测。
