# 稳定性修复清单(2026-08-30 审计修复,Phase 0+1 合并交付)

> 修复基于三路代码审计(/tmp/notion2api_audit_stream.md、audit_dispatch.md)+ 直接代码复核。
> 每条均含:问题 → 文件:行号(修复前)→ 修复方式 → 回归测试。

## P0 — 进程崩溃 / 全线不可用 / 重复输出

| # | 问题 | 修复 | 测试 |
|---|------|------|------|
| A-1 | **半开探测是死代码**:`resolveDispatchCandidatesWithPool` 无 eligible 候选时直接返回错误,dispatch 在错误处返回,永远走不到半开分支;且容量检查在半开之前 → 全员冷却期最长 30min 全线 502 | 新增 `errNoEligibleAccounts` 哨兵 + `buildHalfOpenCandidates`(排除 disabled、按冷却到期升序);dispatch 在 `errors.Is(err, errNoEligibleAccounts)` 时回落半开候选再查容量;删除两处不可达死代码 | `TestResolveDispatchCandidates_AllCooling_ReturnsSentinel`、`TestBuildHalfOpenCandidates_*` |
| A-2 | **续聊 pinning 无 fallback**:会话绑定的账号被禁/冷却/删除后,续聊请求永远 502(STATUS 记录"清空 conversations 表才解决"的根因) | 三处续聊分支(main.go:1746/1895/2018 附近)增设 `AllowPinnedAccountFallback=true`;pinned 优先仍可,池内健康账号可兜底 | `TestPinnedAccount_CoolingWithFallback_*`、`TestPinnedAccount_StrictDisabled_*` |
| A-3 | **dispatch 层轮换分支是死代码**:slot 在判断前已释放(`slotAcquired` 恒 false),且与 `executePromptWithRotation` 双份实现、signaling 不一致 | 两处死分支删除,轮换唯一收敛到 `executePromptWithRotation` | `TestIsRotationWorthyError_Table` |
| A-4 | **流式轮换重试无 emitted 护栏**:已吐字后轮换重播 → 客户端收到"前半段 + 重复完整回答" | `executePromptWithRotation` 包装 sink 计数 Text/Reasoning 发射;非空即不重播直接上抛 | `TestExecutePromptWithRotation_NonQuotaError_PassesThrough` |
| A-5 | **anthropic 流式子 goroutine panic = 进程崩溃**,且 panic/converter 提前返回造成 io.Pipe 永久死锁 | producer goroutine 加 `defer recover + pw.Close()`;converter 返回后 `pr.Close()` 打断写端;`<-done` 加 `r.Context().Done()` 兜底 | (间接)converter 测试组 |
| A-6 | **NDJSON 静默无看门狗**:idle 计时只在"已有可见答案"后武装;"200 后永不发行"可挂 900s,keepalive 让两端互相掩盖 | `ndjsonSilenceTimeout=45s` 全程看门狗(每行重置);无可见答案→`errAccountStarved` 换号;已有部分答案→EOF 收尾保已吐内容 | `TestNDJSONSilenceWatchdog_*`(4 例) |
| A-7 | **http.Transport 零值裸配**:无 dial/TLS/响应头/空闲超时,流式 client Timeout=0 | DialContext(10s)、TLSHandshake(10s)、ResponseHeader(45s)、IdleConn(90s)、连接池上限;cache 上限 64 + 淘汰时 CloseIdleConnections | (构建验证) |
| A-8 | **账号运行时状态并发丢失**:dispatch 每请求快照读→改→整体 SaveAndApply 回写,并发请求互相覆盖冷却/配额/失败计数;且请求路径同步写盘 | 新增 `ServerState.MutateAccount`(锁内取最新→fn 变换→整写);dispatch 4 个写点全部改造;`SaveAndApply/ApplyConfig` 拆出锁内版本消除死锁风险 | `TestMutateAccount_ConcurrentNoLostUpdates`(8 goroutine)、`TestMutateAccount_MakeActive` |

## P1 — 明确故障场景

| # | 问题 | 修复 |
|---|------|------|
| B-1 | **每日冷却只写 DB 不刷内存快照**(`markAccountDailyCooldown` 只调 `store.SaveAccounts`),当前进程继续反复打 429 直到重启 | `WorkspaceRotator` 持有 `*ServerState`,冷却经 `SaveAndApply` 落盘+刷快照;4 处构造点切到 `NewWorkspaceRotatorWithState` |
| B-2 | **anthropic 上游错误被吞成"空成功消息"**:converter 跳过非 `data:` 行,客户端拿到无消息的 message_start/stop 骨架 | `liveSSEWriter` 记录真实 status/header;converter 记非 data 行;EOF 时 status≥400 或 JSON error 体 → 发 `error` 事件而非骨架 |
| B-3 | **容量错(本地并发槽被抢)误标账号 failed+冷却**,高并发下池子自我缩编 | dispatch 失败记账前判 `isDispatchCapacityExceededError` → 跳过记账直接试下一候选 |
| B-4 | **失败/冷却状态持久化错误被吞**(`_ = SaveAndApply`)→ 冷却静默丢失 | 失败日志可观测;配合 A-8 |
| B-5 | **scanner.Err 从未检查**:>16MB 行被静默截断成"成功" | converter run() 显式检查,出错发 error 事件并返回错误 |
| B-6 | **自动重登随请求 ctx 腰斩**;失败不打 LastReloginAt 仍卡 5min;pending 标记丢失 | 登录脱离请求 ctx(helperTimeout+30s);失败不设 LastReloginAt;成功 pending 即时 `MutateAccount` 落盘 |
| B-7 | **黑洞窗口覆盖不全**:saveContinuationScaffold/deleteThread 等直发 postJSON 无超时 | `syncThread`/`syncThreadMessages`/`saveContinuationScaffold`/`deleteThread` 本体内置 bestEffort 窗口 |
| B-8 | **tool_use-only 回合被判失败**(模型本轮只调工具无文本)→ 整轮丢弃 ToolUses | RunPrompt/RunPromptStreamWithSink 检测 `ToolUses>0 && text==""` → 返回合法结果 |
| B-9 | **probe.json 非原子写**:崩溃留下半截 JSON,账号被静默除名 | `writePrettyJSONFile` 改 temp+fsync+rename(Go≥1.21 Windows 也可原子替换) |
| B-10 | **无 graceful shutdown**:重启即掐断所有在途流式请求 | SIGINT/SIGTERM → 30s graceful drain;IdleTimeout=120s;不写 WriteTimeout(会掐流式) |

## 工程性修复

- **`shortID(n)`**:替换 9 处 `strings.ReplaceAll(randomUUID(),...)[ :N]` 裸切片(实现变更即 panic,在 ping goroutine 中=进程崩溃)
- **`.gitignore` 移除 `*_test.go` 排除**——这是发布仓零测试的根因,测试必须入库
- 删除 dispatch 重复的 ~900 行轮换代码残留,轮换单点化

## 测试基线

`internal/app/stability_regression_test.go`:15 个用例全绿;`go vet ./...` 净;`go build ./...` 净。

## 仍未处理(留 Phase 2,不在本次范围)

- 每请求 SaveAndApply 的磁盘成本(现在正确了但仍重——P0-4 的性能侧,建议 Phase 2 引"运行时状态异步批量落盘")
- 注册机/号源自动化链路的 Windows 依赖(Phase 2 核心)
- synthesizer 关键词触发的脆弱性(Phase 3)
- `debug_upstream=true` 默认值会把用户 prompt 落盘(隐私,建议关)

---

# Phase 2(2025-08-30 第二轮):无人值守自动化闭环

| # | 项 | 说明 |
|---|-----|------|
| 1 | **注册机去 Windows 硬编码** | `register_provider.go` 重写:`script_dir/script_name/python_bin/proxy/timeout` 全部走 `register.*` 配置;脚本未配置返回 503 `register_not_configured` 而非崩溃;P0-4 同款的 `a.State.Config` 无锁读已修(锁内去重 + saveAndApplyLocked) |
| 2 | **池水位自动巡检(`startAccountReconcilerLoop`)** | 每 `register.check_interval_sec`(默认 600s)统计健康账号数,低于 `register.min_healthy_accounts`(默认 1)自动调注册机补 1 个;热更配置即时生效 |
| 3 | **healthz 分级** | 新增 `pool_healthy/pool_total/pool_ready`:`session_ready`(进程会话就绪)≠ `pool_ready`(有健康账号可应答);编排层应看 `pool_ready` |
| 4 | **`/internal/metrics` expvar 端点** | 带 API Key 鉴权;暴露 dispatch/上游/缓存全部内置计数器 |
| 5 | **`debug_upstream` 默认关** | config.go 默认 + config.docker.json/config.example.json;开启会把用户 prompt 原文落盘(隐私) |
| 6 | 回归测试 | +4 例(email 提取/默认值/健康统计/healthz 分级字段),全套 `-race` 净 |

## Docker 部署(Linux,docker-compose)

```bash
# 1. 构建并启动
docker compose up -d --build

# 2. 号源(可选):把注册机目录挂载进容器并开启 register.enabled
#    config.docker.json:
#    { "register": {
#        "enabled": true,
#        "script_dir": "/register",     # 挂载点
#        "python_bin": "python3",
#        "min_healthy_accounts": 3,     # 池水位:跌破 3 个健康号自动补
#        "check_interval_sec": 600
#    }}
#    compose volumes 增: ["/path/to/register:/register:rw"]

# 3. 健康检查
curl -s http://127.0.0.1:8787/healthz | jq .pool_ready
curl -s -H "Authorization: Bearer $API_KEY" http://127.0.0.1:8787/internal/metrics | head
```
