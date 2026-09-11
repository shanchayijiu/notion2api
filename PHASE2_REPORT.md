# PHASE 2 REPORT — 低延迟 & Streaming

> 范围：请求级 timing instrumentation、metrics、Streaming 零缓冲复核、低延迟开关。
> 前提约束（用户拍板）：**保留现有默认值，只提供开关**；**没有 benchmark 不宣称提速**。

## 1. 交付内容

### P2-1 request_id + timing instrumentation
- `internal/app/timing.go`（新增）：
  - `requestTimer`：`request_received`（Start）、`first_token`（TTFT）、`last_token`、`model`、`account`、`retries`、`stream_error`。
  - 通过 `context` 随请求传递（`withRequestTimer` / `requestTimerFromRequest`）。
  - `MarkToken()` 首次写 first、之后更新 last；`HasToken()` 避免 Windows 低分辨率时钟把亚毫秒 TTFT 抹成 0 而漏记。
- `internal/app/main.go`：
  - `ServeHTTP` 生成/沿用 `X-Request-Id`（响应头回显，可关联日志），记录完成日志：
    `[req] id=... method=... path=... status=... model=... account=... ttft_ms=... total_ms=... retries=... stream_error=...`
  - Chat 非流式/流式、Responses 非流式/流式的首个内容/推理增量处 `MarkToken()`；`SetModel/SetAccount` 同步。
  - Chat 流式错误分支 `MarkStreamError()`。

### P2-2 metrics
- `internal/app/metrics.go`：新增直方图 `notion2api_ttft_seconds`（buckets 0.1s→34s），随 `/metrics` 暴露；`resetMetricsForTest` 同步重置。
- 总延迟沿用既有 `notion2api_request_duration_seconds`（按 path/method/status）。

### P2-3 Streaming 零缓冲复核 + 低延迟开关
- 复核结论：`writeSSEData` 每个 payload 都 `flusher.Flush()`（`main.go:3081-3087`），Chat/Responses SSE 均逐块 flush，无整段缓冲（`stop` 序列触发的显式缓冲除外，属既定语义）。
- 新增低延迟开关（默认不变）：`streaming.initial_flush_delay_ms`
  - 缺省（字段不存在）→ 沿用历史 1500ms。
  - `0` → 收到首个 token 立即发；适合反向代理/浏览器超时可控的部署。
  - `resolveInitialFlushDelay(cfg)`；`(*App).initialFlushDelayFor(request)` 替换旧的包级函数。
- 未改动 `features.use_web_search` 默认值（仍是 1）；仅保留既有开关。

## 2. 测试

| 文件 | 内容 |
|---|---|
| `internal/app/timing_test.go` | `X-Request-Id` 回显/生成、`MarkToken` 首次一次、流式请求记录 TTFT metric、`resolveInitialFlushDelay` 开关（默认/0/250ms） |

## 3. 验证结果

```
go build ./...            PASS
go vet ./...              PASS
go test ./... -count=1    PASS（含 Python SDK 冒烟）
```

## 4. 性能

**本阶段未做 live benchmark**，因此不声明任何提速数字。instrumentation 就绪后，需在真实实例上执行下述命令产出 `BENCHMARK.md`（Phase 7）：

```bash
# 1) 起服务后跑同一 prompt（流式）
curl -N -s -D - -X POST http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"PONG"}]}' \
  | ts '%H:%M:%.S'            # 观察首字节/块间隔
# 2) 抓服务日志中的 [req] ttft_ms/total_ms
# 3) 对比 streaming.initial_flush_delay_ms = 1500 vs 0
# 4) 抓 /metrics 的 notion2api_ttft_seconds 直方图
```

参考基线：`STATUS.md` 记录上游 TTFB p50 ≈ 2.9s（属上游 profile，非网关缓冲）。

## 5. 未做（按计划后移）

- 真·增量 tool_calls 事件、按事件类型驱动的 Stream Processor → Phase 4/5。
- Session 缓存 / 减少续聊往返 / 每请求 config 落盘 → Phase 3。
- 上游 TTFB 独立测量（transport 层 first-byte 打点）→ 可在 Phase 4 事件模型接入时补。

## 6. 下一步建议

Phase 3（Session Runtime）：内存 `SessionInfo` 缓存、acquire/release、合并续聊的 +3 上游往返、避免每请求写 config；复用现有 transport/session，不重写 `notion_client.go`。
