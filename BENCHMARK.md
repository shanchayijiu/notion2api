# BENCHMARK

> 状态：**未执行 live benchmark**。这里只规定方法，不写任何未经测量的数字。
> 依据：改造原则"没有 benchmark 不宣称性能提升"。

## 测量项

| 指标 | 含义 | 来源 |
|---|---|---|
| TTFB | 上游连接/首字节 | 需 provider 层打点（Phase 4 接入） |
| TTFT | 首个 streamed token | `[req] ttft_ms` / `notion2api_ttft_seconds` |
| Total | 请求总耗时 | `[req] total_ms` / `notion2api_request_duration_seconds` |
| 成功率 | 2xx / 全部 | `notion2api_request_duration_seconds` 分 status |
| Retry | 账号/session 重试次数 | `[req] retries` |

## 前置条件

- 至少 1 个健康账号（probe 完整），服务以 `--config` 启动。
- 建议关闭 `features.use_web_search` 以隔离联网噪声（对比实验）。

## 步骤

```bash
# 0) 起服务
go run ./cmd/notion2api --config ./config.example.json

# 1) 热路径非流式（10 次），记录 duration
for i in $(seq 1 10); do
  curl -s -o /dev/null -w '%{time_total}\n' -X POST http://127.0.0.1:8787/v1/chat/completions \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"model":"gpt-5.4","messages":[{"role":"user","content":"PONG"}]}'
done

# 2) 流式首字节 / 块间隔
curl -N -s -X POST http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"PONG"}]}' \
  | while IFS= read -r line; do printf '%s %s\n' "$(date +%H:%M:%S.%3N)" "$line"; done

# 3) 汇总服务端 [req] 日志
grep '\[req\]' server.log | tail -50

# 4) 抓 Prometheus 直方图
curl -s http://127.0.0.1:8787/metrics | grep -E 'ttft_seconds|request_duration_seconds'
```

## 对比项

- `streaming.initial_flush_delay_ms`: `1500`（默认） vs `0`。
- `features.use_web_search`: `true` vs `false`。
- Session 缓存前（Phase 2 基线） vs 后（Phase 3）。

## 已知上游基线

`STATUS.md` 记录 Notion 上游 TTFB p50 ≈ 2.9s（上游首 token 延迟，非网关缓冲）。网关侧不做缓冲（`writeSSEData` 逐块 `Flush`）。

## 结论

待在有健康账号的实例上执行后填写。**在此之前，任何性能声明都视为无效。**
