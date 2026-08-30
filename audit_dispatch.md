# 审计报告:「请求调度与账号池」链路稳定性缺陷

审计范围:`internal/app/request_dispatch.go`、`account_pool.go`、`session_refresh.go`、`account_discovery.go`、`conversations.go`(会话绑定)、`sqlite_store.go`(账号/会话持久化),并交叉阅读了 `main.go`(槽位/ pinning / SaveAndApply)、`notion_client.go`(黑洞检测)、`login_helper.go`、`workspace_rotation.go`。

## P0 — 会直接导致用户请求失败/挂死/换号失效

### P0-1 「半开探测」是无效死代码:全员冷却 = 全线 502,最长 30 分钟
- **位置**:request_dispatch.go:485-511 与 701-727(两个 dispatch 变体同病)
- **问题**:注释声称"全员冷却时按冷却到期最早者放行一次"(STATUS.md 也宣称已实现),但 `resolveDispatchCandidatesWithPool` 在无 eligible 候选时直接返回 `noEligibleAccountsError`(account_pool.go 经由 request_dispatch.go:166-168),于是 dispatch 在 485 行 `if err != nil` 处直接返回,永远走不到 498 行的半开分支。即使错误被放行,488-494 行的容量检查在 `candidates` 为空时 `candidateEmails` 为空 → `AvailableDispatchCapacity` 返回 0 → 提前返回 `noDispatchCapacityError`,半开分支依然不可达。双重挡死。
- **失败场景**:所有账号因上游标记/限流进入冷却(偶遇上游风暴或 STATUS 所述"新号 ~5 次/小时即限流"的现实下极易发生)→ 冷却期内(最长 30min)所有客户端请求直接 502/报错,服务整体不可用,与 7x24 目标直接冲突。
- **修复建议**:把半开候选的构造挪到错误返回之前——`resolveDispatchCandidates` 返回 noEligible 时回落到"按 CooldownUntil 升序的全部可用制品账号";并把半开候选纳入容量检查。冷却语义改为"优先跳过"而非"硬排除"。

### P0-2 会话续聊 pinning 无 fallback:绑定已死账号 → 硬错误 502
- **位置**:main.go:1746/1895/2018(`request.PinnedAccountEmail = conversation.AccountEmail`,未设 `AllowPinnedAccountFallback`)+ request_dispatch.go:185-196
- **问题**:续聊请求把会话历史 `AccountEmail` 固定为 pinned 账号且不允许回退。`resolveDispatchCandidatesWithPool` 非 fallback 分支中:账号 disabled → 直接返回错误;无制品 → 直接返回错误;**且不检查冷却**(cooldown 只对 pool 路径生效)。若 pinned 账号被上游标记,请求以 `errAccountStarved` 失败后候选列表只有一个账号,循环结束,user 拿到 502。这正是 STATUS.md §"循环工作流 #7 补记"中"conversations 表旧会话绑定已死账号 → 清空表才解决"的代码根因,至今未修(其"遗留②"也自认未修)。
- **失败场景**:账号 A 完成某会话后被禁用/删除/长期冷却;用户在该会话继续提问 → 每条续聊请求都 502,且错误信息是"account X is disabled",用户无感知、无法自愈;重启后 sqlite 中的绑定依然指向死账号,问题永久化。
- **修复建议**:续聊 pinning 默认设 `AllowPinnedAccountFallback=true`(或 pinned 失败为 starved/disabled/冷却时自动降级为普通池调度);UI/admin 侧提供"解绑/重绑会话账号"操作;pinned 账号冷却时视为不可 pinned 而非放行。

### P0-3 dispatch 层的「限制类错 → 轮换续聊」分支不可达(死代码),且与 executePromptWithRotation 重复实现
- **位置**:request_dispatch.go:555-558(slot 已先释放)→ 623 `&& slotAcquired`;以及 sink 变体 785-787 → 857
- **问题**:rotation 重试块条件中含 `slotAcquired`,但 slot 在 555-558 行已被释放并置 false,该条件恒为 false——两段 rotation 分支永不执行(857 变体里还使用原始 `sink` 而非 wrapped sink,即使触达也有 emittedAny 追踪失效/重复输出问题)。真正生效的轮换只有 account_pool.go:320 `executePromptWithRotation` 里的一套。两处轮换语义(冷却策略、slot 生命周期、成功后的账号持久化)不一致,后续维护者极易改错其中一套。
- **失败场景**:不是直接的用户可见故障,但意味着"dispatch 层策略"(标记失败、冷却、换号)与"轮换策略"实际只剩单点实现;若哪天有人"修复"了 623 的条件,立即引入 slot 双释放(inflight 被减成负数后被钳到 0,破坏并发计数)和流式重复输出。
- **修复建议**:删掉 dispatch 层 623-644 / 855-878 死分支,轮换只保留 `executePromptWithRotation` 一处;或明确分层并把 dead code 连同注释一起移除,避免误导。

### P0-4 dispatch 循环内同步 SaveAndApply(配置写盘 + sqlite)阻塞请求路径,且多并发请求互相覆盖配置
- **位置**:request_dispatch.go:548/594/634、662(成功路径每请求一次;失败路径每候选一次)、main.go:530-563(SaveAndApply 无锁 read-modify-write)
- **问题**:(a) SaveAndApply 内做 `saveConfigFile`(磁盘写)+ `Store.SaveAccounts`(sqlite 全量写账号表),在**请求关键路径同步执行**;sqlite 写入与 `persistConversationSnapshot` 等并发争用时延迟可达百毫秒级,直接叠加到用户 RTT;(b) 每个 dispatch goroutine 各自 `Snapshot()` 出 cfg 副本、本地修改、整体 SaveAndApply——两个并发请求处理同一账号(或不同账号)时后写覆盖先写,导致并发请求累计的 `WindowRequestCount`、`ConsecutiveFailures`、`CooldownUntil` 丢失,配额/冷却统计系统性失真(并发越高越失真)。
- **失败场景**:高峰期账号失败计数被并发覆盖 → 冷却不按预期生效(该隔离的号继续被打)→ 上游把账号彻底标记;或配额窗口计数丢失 → 超打触发日限额。也出现过调试期"明明失败了冷却却不生效"类怪异现象的根源。
- **修复建议**:账号运行时计数(成功/失败/冷却/窗口)与静态配置分离,改用带互斥锁的内存运行时表 + 异步批量落盘(落盘仅作重启恢复);SaveAndApply 移出请求路径(队列化、合并写)。

## P1 — 偶发/特定条件下出错

### P1-1 `executePromptWithRotation` 流式重试不检查"已吐字",可能向客户端重复输出
- **位置**:account_pool.go:320-348
- **问题**:注释声明"流式场景:只在未吐出任何内容前失败才重试",但实现没有任何 emitted 检查——`execute` 返回 rotation-worthy 错即 Rotate 并重放整请求。流式执行中 sink.Text 已经把增量发给客户端,重放会从头再生成一遍文本。
- **失败场景**:长回答流式输出中途遇到 quota-exhausted/temporarily-unavailable → 客户端看到"前半段 + 重复完整回答"拼接文本。
- **修复建议**:给 execute 包一层 emitted 计数闭包(可在 runPromptWithSessionWithSink 包装 sink 时顺手做),已吐字则不轮换,直接把错误上抛。

### P1-2 刷新重试时槽位被其他请求抢走 → 健康账号被误标 failed + 冷却(误杀)
- **位置**:request_dispatch.go:574-577 与 803-805(`TryAcquire` 失败 → `err = noDispatchCapacityError; retryable = false`),随后 660/894 `markAccountDispatchFailure(retryable=false)` 把 Status 置 "failed" 并进入冷却
- **问题**:容量不足是**本地并发状态**,不是账号健康信号,却被当成账号失败记账。
- **失败场景**:高并发下账号 A 的 401 触发刷新重试,前一瞬 slot 被另一请求占走 → A 被标 failed + 冷却,池子在高负载时"自我缩编"。
- **修复建议**:容量类错误单独记账(不增加 ConsecutiveFailures、不置 failed、不设冷却),直接换下一个候选。

### P1-3 轮换(rotate)后旧会话仍绑定旧空间 thread,续聊必失败
- **位置**:conversations.go:1035-1091(persistConversationSession 记录 account_email + thread_id,不记 space_id)+ workspace_rotation.go Rotate(换新空间)
- **问题**:轮换引擎给账号开新空间后,sqlite 里 `conversation_sessions.thread_id` 仍指旧空间的 thread;续聊时 `request.UpstreamThreadID` 拿旧 thread 在新空间 sync → 上游报 thread 不存在/黑洞 → 请求失败。
- **失败场景**:账号被限 → 自动轮换成功 → 用户继续同一会话 → 每轮续聊 502 或黑洞超时,用户体感"会话突然就坏了"。
- **修复建议**:session 持久化增加 space_id 并在加载时校验"会话 thread 的 space == 账号当前 space",不匹配则按 freshThread 重放(main.go:1747-1749 已有 ForceLocalConversationContinue 机制可复用)。

### P1-4 会话/账号运行时状态与持久化状态的重启不一致
- **位置**:request_dispatch.go probeCache(纯内存)、sqlite_store.go:109-124/253(accounts 表)、config 文件双写
- **问题**:(a) accounts 同时持久化到 config 文件(SaveAndApply)与 sqlite accounts 表,两份真相,启动加载顺序若不一致(代码库 STATUS 中多次出现"重启后又要清表"即症候)会出现旧状态复活;(b) probe/refresh 的中间状态只在内存,崩溃重启后 `pending_code` 状态的账号永远停在 pending(没有启动时的状态收敛);(c) `Status` 字段(enabled/disabled/failed/expired/pending_code)由多处分散写入,无单一状态机。
- **失败场景**:进程重启(崩溃/升级)后:卡在 pending_code 的账号长期占位;"failed" 旧标记在换上健康 cookie 后仍未复位,需要手工清表。
- **修复建议**:启动时对账号状态做一次 reconcile(pending_code 超时回退、cooldown 过期清标记);明确"config 文件 vs sqlite"谁是 accounts 的权威源,另一处只作缓存。

### P1-5 tryRefreshAccount 写 probe.json 非原子 + 与周期刷新协程并发写同文件
- **位置**:session_refresh.go:142-165(writeSessionArtifacts → writePrettyJSONFile)、login_helper.go:115-128(直接 os.WriteFile,无 tmp+rename)、session_refresh.go:346-381(周期循环只串行 refreshMu)vs request_dispatch.go:565/795(dispatch 内 tryRefreshAccount **不持 refreshMu**)
- **问题**:dispatch 路径的 refresh 与 StartSessionRefreshLoop 的周期 refresh 可并发跑,二者向同一 `probe.json`/storage_state 文件并发 `os.WriteFile`;非原子写下崩溃/交错可留下截断 JSON → 下次 loadSessionInfo 解析失败 → 账号被判 missing_artifacts 而除名(账号在配置完好时被静默踢出池)。
- **失败场景**:请求触发刷新的同一时刻周期刷新到点 → probe.json 写坏 → 该账号后续请求全部 loadSession 失败直至人工介入。
- **修复建议**:writePrettyJSONFile 改 tmp+rename 原子写;按账号加 per-account refresh 互斥锁(dispatch 路径与周期路径共用)。

### P1-6 startAutoRelogin 用请求 ctx 驱动浏览器登录流,请求结束后流程被腰斩
- **位置**:account_pool.go:234-260 + request_dispatch.go:653-658/886-892
- **问题**:`StartEmailLogin` 拿的是带 60s 预算/随客户端断连取消的请求 ctx;登录流(发验证码、等用户/注册机回填)天然分钟级。ctx 取消后登录流程中止但 pending 标记已写,且 `LastReloginAt` 已更新 → 5 分钟内(accountAutoReloginInterval)新的重试被 `accountReloginRecentlyStarted` 挡掉。
- **失败场景**:账号 401 → 自动重登被请求结束掐死 → 连续多个请求都因"recently started"不再尝试 → 该账号长期 expired,需人工 admin 操作。
- **修复建议**:relogin 用独立的后台 ctx(detached),并容许失败时立即重置 LastReloginAt,使下一请求可重试。

### P1-7 `errAccountStarved` 的流式判定只在"完全没有 agent-inference 行"时触发,慢启动/半截推理不被识别
- **位置**:notion_client.go:4221-4222、4291-4292、2660(HasAgentInference 设置点)、流循环 2820-2842
- **问题**:HasAgentInference 一旦出现即视为健康;若上游出现"起了 inference 但永不吐完"的新形态黑洞(STATUS 记载的标记行为已经演变过一次),流式路径只剩下 idleAfterAnswer 定时器,而它在 `!state.hasVisibleAnswer()` 时不启用(2801)——"有 agent-inference 但无可见文本"的挂起形态只能靠 60s 总预算兜底,超过预算后被 `isDispatchContextAbort` 当作上下文中止直接返回(559-561),**不进入换号逻辑**,而该形态恰恰是账号级故障。
- **失败场景**:上游行为再次漂移(已有先例)→ 所有请求吃满 60s 预算后 504/超时,不切换账号,被标记账号不被冷却。
- **修复建议**:加"agent-inference 启动后 N 秒无任何可见输出"的 stall 检测,命中时按 starved 处理(冷却+换号)。

## P2 — 健壮性/可维护性

### P2-1 HourlyQuota 只参与排序不参与资格判定,仍会烧超配额号
- **位置**:account_pool.go:50-60(accountRemainingQuota 仅 sortDispatchCandidates 使用)、77-90(accountDispatchEligible 不看配额)
- **问题**:配额耗尽的账号仍然 eligible,只是排序靠后;候选少时照样被选中,撞上游限流 → 计为失败 → 进入冷却,浪费请求预算与账号寿命(STATUS 明确"每号 ~5 推理/小时")。
- **修复建议**:配额耗尽时判 ineligible(或大幅降低优先级 + 半开式低频探索),把配额窗口重置逻辑与调度统一。

### P2-2 冷却时长语义反直觉:非 retryable(账号级硬失败)冷却减半
- **位置**:account_pool.go:92-108(`if !retryable { wait /= 2 }`,下限 30s)
- **问题**:errAccountStarved 这类最明确的"账号被标记"信号拿到的是**最短**冷却(30s),意味着被标记账号每 30s 就会再被试一次、白白消耗一次探测;而真正偶发的 401(retryable)反而冷却更久。
- **修复建议**:交换/拉平语义,或按错误类别显式配置冷却时长并对 starved 用指数退避到 30min 顶格。

### P2-3 冷却字符串持久化对时钟回拨/解析失败零防御
- **位置**:account_pool.go:18-28/62-70(parseOptionalRFC3339 解析失败当无冷却)、request_dispatch.go:506-508 排序把零值(解析失败)排最前
- **问题**:配置被手工编辑或跨时区写入非法时间串时,冷却静默失效,坏号立即重新被打。
- **修复建议**:解析失败记日志并按"仍冷却"保守处理;持久化同时写 Unix 秒字段做交叉校验。

### P2-4 probeAccountProtocolHealth 把"上下文中止"当作探测成功
- **位置**:request_dispatch.go:293-296、304-306
- **问题**:客户端断开/预算到期导致的探测中断会 `markAccountProtocolProbeSuccess`,随后 TTL 内(ProbeCacheTTLSeconds)跳过真实探测——用没探测过的账号当"刚探测过健康"记账。
- **失败场景**:高频取消场景下探测缓存长期显示健康,真正的协议级黑洞掩盖到 runPrompt 才暴露(多花一次 8s/20s 黑洞检测)。
- **修复建议**:context abort 时不写缓存(既不算成功也不算失败)。

### P2-5 死号 metadata 修复依赖 chooseBestSpace 的启发式打分,可能选错空间
- **位置**:account_discovery.go:57-109(AIEnabled +2、非 free +1、有名字 +1)
- **问题**:多空间账号refresh/discovery 时可能选到一个 AI 已禁用但 plan 更高的空间(trial/business 空间 STATUS 已实测"不可推理"),选错后账号被判 starved 冷却,而真正的 personal 空间没人用。
- **修复建议**:空间选择记录来源与置信度;trial_not_allowed 信号直接进入打分负项;选错后支持 rotate 纠正(现有 rotator 机制)并持久化纠正结果。

### P2-6 失败路径 `markAccountDispatchFailure` 把 retryable 错误置 Status="expired",会误导 admin UI 与人工处置
- **位置**:account_pool.go:141-147
- **问题**:一次偶发 401(retryable)就把账号显示为 expired;STATUS 中多次出现"人工看状态停用账号"的操作流,这个状态噪音会诱导误停用健康号。
- **修复建议**:Status 区分"临时错误"与"会话失效",或引入 last_error_class 而非复用 Status。

### P2-7 NDJSON 扫描 goroutine 的退出依赖调用方关闭 done;若未来新增早退分支易泄漏
- **位置**:notion_client.go:2740-2843(events/done 协议)
- **问题**:当前无泄漏证据,但退出路径(错误、idle、ctx 取消)依赖每条 return 前 reader/done 被妥善处理,协议隐式。
- **修复建议**:用 `defer close(done)` + 明确注释不变量,或以 errgroup 重构。

### P2-8 `isSessionRetryableError` 纯字符串匹配,误伤面大
- **位置**:session_refresh.go:21-47(任一错误文本含 "session"/"forbidden"/"login" 即 retryable)
- **问题**:上游业务报错只要文案含 "session"(例如 "session record not found")就会触发一次完整刷新(bootstrap + getSpacesInitial,各一次上游往返),拖慢失败返回并给上游额外压力。
- **修复建议**:retryable 判定以 HTTP 状态码为主、消息匹配收窄(精确子串/错误码枚举)。

---

## 汇总建议(按投入产出比)
1. 先修 **P0-1**(半开死代码)和 **P0-2**(pinning 无 fallback)——这两条各自复现过线上 502,修复量都很小。
2. 删 **P0-3** 死分支,消除"双套轮换"的认知陷阱。
3. **P0-4** 决定长期可维护性:账号运行时状态从 config 拷贝里拆出去。
4. P1-1/P1-3/P1-5 只有在你真的跑 7x24 且账号会被限流时才会咬人——鉴于 STATUS 里"配额硬约束"是常态,建议一并排进下个迭代。
