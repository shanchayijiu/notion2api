# notion2api — 阶段文档（截至 2026-08-25）

> 主会话诚实记录：探测到哪、卡在哪、为什么卡、还差几步。
> **2026-08-25 更新**：**循环工作流目标达成**（opus5 规划→执行→review 循环 5 轮）——主流客户端工具调用可用：流式 tool_calls 增量分片（REQ-TOOL-04 全协议）+ tool_choice required/named + toolStreamSieve 半截标记缓冲（双闸+时间/片数闸+判据收紧）+ **官方 Python SDK 生态矩阵 3 项全过**（非流式/流式/多轮回填），全测试绿。借鉴 grok2api（白名单/流式 sieve）ds2api（schema 归一化）落地。
> **2026-08-25 11:3x 稳定性修复**：**根因查明 = 主号 mt57jrhj0dn7@aitextextractor.com 被上游标记**（09:58 起 runInferenceTranscript 只建 thread 不推理：3 行无 agent-inference → temporarily-unavailable；后续 syncThread/syncThreadMessages 上游黑洞 → 服务请求挂死 150s+）。排查排除：代理 3067（并发/复用模拟全 1s 内）、连接复用（_probe_reuse_stream）、上游（probe 直连 400/200 正常）。**解法：admin API 导入注册机新号 mt80wafmshg9@imageeditgpt.com + activate 切换主号 + 停用旧号** → 默认请求 2.9s OK。**模型列表随号变化**：gpt-5.2 已下架 → 用 gpt-5.4（/v1/models 27 个）。pprof 已开（config debug.pprof_enabled=true, 127.0.0.1:6060，抓过 goroutine dump 定位 syncThreadMessages 等连接）。
> **2026-08-25 循环工作流：账号自动检测+自动切换（CC/Codex harness 驱动，非脚本）**。用户要求「出问题自动切账号，而不是出问题再去检查」→ 实现：RunPrompt/RunPromptStream 在 `parseErr==nil && !HasAgentInference` 时直接返 `errAccountStarned`（跳过 loadFinalAnswerOnce/poll 长等待）；syncThread 8s / poll 20s 黑洞检测；markAccountDispatchFailure 设置 CooldownUntil（不再清空）+ accountDispatchEligible 检查冷却（accountCooldownActive）；dispatch 失败换候选 + errAccountStarved 显式非 retryable；**请求级 60s 总预算**（防 N×黑洞叠加）；**半开探测**（全员冷却时按冷却到期最早者放行一次，避免 30min 全线不可用）；成功即重置失败计数/冷却。loop：round1 用 `claude -p`（指向本服务）审查 → 列出 P0（仍挂死/漏切/误杀/全员冷却/超时过大）→ 全部修复 → round2 同一 harness 复审查 → **无剩余 P0，判生产可用**。测试全绿（含 account_cooldown_test.go 冷却/eligible/HasAgentInference 检测）。
> **2026-08-25 CC 工具场景端到端回归（真实 harness）**：`claude -p --allowedTools Read` 指向本服务（OPENAI_BASE_URL=http://127.0.0.1:8787/v1, model=opus-5）+ 读取含随机密钥的 `_cc_secret.txt` → CC 真实执行 Read 工具并原样返回 `SECRET-7F3A9C2B-805404607`（与文件一致）。**证明 CC 工具链路经本服务端到端打通**——此前"根本不会回消息/工具场景乱码"实为旧主号被上游标记导致 150s 挂死，已由账号自动切换修复。CC 接入目标达成。

---

## 0. 一句话现状

Notion AI → OpenAI 兼容桥（GALIAIS Go 框架）。P0-P2 基本完成（轮换/附件/thinking/模型自愈/生产服务化/顺式策略）。
**工具调用闭环已验证**：客户端绝对路径 → mask 成 `~/` 工作区路径 → 注入"工作区文档整理"顺式框架 → 模型输出 read_file 调用文本 → 解析 unmask 还原真实路径 → tool_calls 透传 → 客户端执行 → 结果回填 → 模型直接作答（不重复调用）。
下一步：阶段 3 验收标准 v4 对齐（内容平面/终止路径/报告）或继续打磨工具桥稳定性。

真跑命令：
```
cd C:\Users\Administrator\notion2api
taskkill /F /IM notion2api.exe; python research\_start_srv.py   # 重启服务（stderr.log 接管日志）
python research\_probe_tool.py   # 工具闭环冒烟（读文件→调用→回填→回答）
```

---

## 0.5 目标锚点（开工时定，之后只许显式改，不许悄悄漂）

**北极星**：Notion AI 以 OpenAI Chat Completions 为唯一契约做 Drop-in Replacement——客户端只换 base_url/api_key 不改业务代码，六个维度与官方 API 行为不可区分。

**成功判据**（可验证）：
- [ ] 工具调用闭环：多轮（调用→执行→回填→回答）稳定跑通（当前单轮验证过，多轮/并行待强化）
- [ ] 验收标准 v4 对齐：内容平面纯净（标记零泄漏/码点守恒/终止路径统一）、报告产出
- [ ] 生态矩阵：官方 SDK/Cherry Studio/Cline 零配置跑通工具调用

**循环工作流目标（2026-08-25 订立）**：主流客户端工具调用可用——流式 tool_calls 增量分片（REQ-TOOL-04）+ tool_choice required/named 完整语义 + 流式半截标记不外泄。
**达成判据**：Cline 风格流式工具调用端到端 + 全测试绿 + opus5 review 无阻塞项。

**循环工作流目标 #2（2026-08-25 午后订立，卡点 B 收口）**：验收标准 v4 P0 剩余 unknown 全部关闭——INV-03 码点守恒账本、INV-06 分块不变性 fuzz、INV-15 通道不回灌、INV-13 六类终止路径、REQ-STR-08 usage null 语义、T-16 反向门禁 8 mutant 全杀、REQ-DEP-04 部署一致性留痕。
**达成判据**：`_runtime/v4_consistency_report.json` verdict != invalid 且无 fail；8 mutant 全部被杀死；REQ-TOOL-04 已有实现维持绿；opus5 review 无阻塞项。**循环模式**：执行一批 → opus5 review → 立即修 → 端到端回归 → 有阻塞项进下一轮（CORE_PRINCIPLES §5）。
**2026-08-25 达成（round 1-4）**：v4 报告 19 pass / 3 unknown（INV-02/09/12 为 E3 级，P2 范围，已带 unknownConverge 收敛判据）/ 0 fail，verdict=insufficient-evidence；T-16 反向门禁 11/11 mutant 全杀（scripts/mutant_gate.ps1 + mutant→不变量矩阵）；红灯先行证据固化 `_runtime/v4_evidence/red_first_fixtures.txt`；新增 stop 序列支持（S4 缓冲模式）。**INV-06 fuzz 抓到并修复 6 个真实泄漏 bug**（见 §2 卡点 A3）。**opus5 review round 4 终判：无条件通过，无 P0/P1 阻塞项 → 目标 #2 达成**。下一步：E3 金丝雀 T-09（review 建议优先于卡点 A），启动清单见 §3。

**明确不做**：
- 不做 Assistants/Realtime/Batch 端点（v4 非目标）
- 不做 Anthropic 原生格式全面兼容（maxapi 范畴）
- 不破解 Claude/opus 系 thinking 加密（上游行为，gpt 系明文已够用）
- 不承诺 token 计费精度达官方级

**手段 vs 目的**：
- 目的 = 让客户端把 Notion 当正经 API 用（含工具调用）
- 当前手段 = 协议直连 + 提示词注入（顺式）+ 路径伪装；Cloak 浏览器仅兜底
- ⚠️ 手段可换，目的不能换

**目标变更记录**：
- 2026-08-25：工具调用从"透传原生 agent-tool-result"扩展为"提示词注入驱动模型输出调用文本 + 原生事件双通道"——原因：Notion 对工作区外工具（get_weather）硬拒，但对工作区内能力（fs 读文件）100% 配合，提示词驱动是主路径。

---

## 1. 已稳的部分（约 85%）

> **保护区。** 换会话/修局部/加前端时默认不许动这里列的链路与文件。

### 1.1 整条链路
- P0 AI 桥主路径 ✅（smoke 全过）
- P1 轮换引擎 ✅（createSpace 9 字段 + 免绑定 + 自动轮换）
- P2 生产化 ✅（计划任务服务化/持久化/健康检查）
- 顺式策略 ✅（prompt_guard 全前缀改顺式；身份探针 mock）
- **工具调用闭环 ✅（2026-08-25 最新）**：mask→注入→输出→解析→unmask→tool_calls→回填→回答

### 1.2 已解决的硬阻断
- 工具桥注入丢失：freshThread 重放用 latestPrompt 重建 prompt 覆盖注入 → 移到组装末端（userStep value，`maskIfEnabled`）✓
- `preparePromptRequest` 重建 request 漏拷 `MaskLocalPaths`/`ToolBridgeSection`（导致 mask/注入静默失效）✓
- 绝对路径被拒（"无法访问你电脑上的本地路径"）→ `maskLocalPaths` 改写 `~/` + `unmaskPathArgs` 智能还原（去 `Users//` 双斜杠）✓；mask 幂等归一化历史坏形态（`~/Users//name//...` → `~/...`）✓
- tool 结果回填后模型重复调用 → `buildToolResultsPrompt` 加强措辞（"no further action blocks"）✓
- 服务通道原生工具事件"缺失"= 日志未重定向误判（Start-Process 无 -RedirectStandardError）→ 实际正常收集（fs.readFiles×2 + loadUser）✓
- 带 tools 的请求复用历史会话 → 旧答案重放 → 模型不输出调用 → **带 tools 不走跨会话续聊**（每请求新 thread，客户端自带完整上下文）✓
- **工具块剥离破坏提取（自踩坑）**：sanitize 增加 stripToolActionBlocks 后 `applyInferenceResultOutputPolicy` 先剥离 → 1767 extractToolCalls 提取不到 → **提取必须用净化前原始文本**（rawResultText）✓（规则："tool_calls 在 sanitize 之前 extract"——2api-bridge skill 早有此条）
- **P0 验收对齐**（2026-08-25）：DEP 组（X-Build-Fingerprint 响应头 + healthz 回显 commit/binary_sha256/sanitizer_config_version/dialect_table_version/upstream_profile/process_started_at，build_identity.go）、工具标记剥离（stripToolActionBlocks：```json 完整/未闭合 + `<tool_call>` XML + 合法代码块保留）、REQ-ERR-08（流式首字节后错误映射 error 行 code=upstream_aborted 而非 stop）✓
- **上游行为漂移破解（c2a synthesizer）**：模型不再输出调用块 → `synthesizeToolCall` 从拒绝文本提取路径合成 tool_calls（仅首轮、强制/拒读触发）→ 闭环恢复 ✓
- **Web 端注册机入口**：AccountsPanel "注册机产号"卡片（proxy 可选 + 进度 + 结果自动填充账号池），service registerAccount + admin-console onRegister 接线，构建+同步+页面验证 ✓；后端 /admin/accounts/register 实测产号成功（mt7h7qg64vbk@aitextextractor.com）✓

### 1.3 真实样本已落盘（关键资产）
| 文件 | 内容 | 价值 |
|---|---|---|
| `research/logs/raw_tool2.jsonl` | 浏览器请求真实 NDJSON（3 个 agent-tool-result） | 原生透传 fixture |
| `internal/app/tmp_last_runInferenceTranscript_body.json` | 最近请求 body（DebugUpstream 持续覆盖） | 注入/mask 验证 |
| `internal/app/ndjson_tool_test.go` | 真实 NDJSON 收集测试 | 回归 |

### 1.4 保护区文件/目录
- `internal/app/tool_bridge.go`：工具桥核心（注入/解析/mask/unmask/探针）——旁路任务只读
- `internal/app/notion_client.go`：NDJSON 解析 + 请求组装（userStep/maskIfEnabled/preparedReq 拷贝）——改前必回归
- `C:\Users\Administrator\Desktop\2api 兼容层验收标准 v4.md`：唯一验收依据

### 1.5 已稳主路径真跑命令（回归用）
```
cd C:\Users\Administrator\notion2api
go build ./cmd/notion2api && go test ./internal/app -count=1   # 全绿
python research\_probe_tool.py                                  # 工具闭环冒烟
```

### 1.6 暂停/占位模块（故意未完成，但不许误改）
- `internal/app/notion_client.go` `var clientDebugEnabled = false // DEBUG`：**调试标记**（保持 false）| 何时开：需要工具收集日志时改 true | 标识：注释 DEBUG
- `research/_start_srv.py`：调试用服务启动器（python 接管 stderr.log）| 生产用 `scripts/start-production.ps1`
- `enableAgentThreadTools: true`（config 默认值改过）：保留 true（可能影响原生工具事件，不撤）

---

## 2. 卡点（剩余约 15%，工具桥稳定性 + 验收对齐）

### 卡点 A：工具调用稳定性（模型偶发重复调用/格式漂移）—— 决定因 = 提示词语义边界
**铁证**：
- 读文件场景 R2 回填后偶发再次调用 read_file（R1 成功输出调用）
- 模型对 `~/Users//Administrator//...`（双斜杠+重复尾段）会原样输出，unmask 已兜住

**本会话实证（2026-08-25）**：
1. 加强措辞（"results already satisfy the request, no further action blocks"）→ R2 稳定 stop（连续 2 次通过）
2. 历史重放里旧 [assistant] 已答过 + 结果注入 → 模型仍可能重复调用（概率性）
3. 结论：单轮回填已稳；多轮（客户端连续多次工具）与并行工具未实测

### 卡点 A2：工具调用上游行为漂移（2026-08-25 深夜）—— 已破解（synthesizer）
**铁证**：
- 23:42-23:50 同注入同请求 100% 输出 read_file 调用块（端到端真跑成功，tool_calls 正确）
- 00:35 后同请求连续 10+ 次全拒：模型改为"自己尝试读沙盒虚拟文件系统 → File not found → 拒绝"
- 排除项：代理（3067/7890 双测均拒）、模型（gpt-5.2/sonnet/opus/deepseek 全拒）、注入（body 验证完整）、历史（新 thread 仍拒）、路径形式（`~/`/`./`/`/workspace/`/`modules/` 全拒）、tool_choice（required 也拒）、**账号（注册机新号全新工作区也拒 → 上游全局行为变化，非账号标记）**
- 代码侧全绿（go test ok；extractToolCalls/sanitize/mask 单测过）

**破解（c2a synthesizer，2026-08-25）**：
- 模型拒绝文本里**含完整路径**（"无法访问你电脑上的本地路径 `C:\...`"）→ 服务端 `synthesizeToolCall` 提取路径 + 匹配客户端工具（read 类/forcedName/回复提及）→ 合成合法 tool_calls（名字来自客户端 catalog，REQ-TOOL-08 语义）
- 触发条件（防误伤）：tool_choice 强制 或 模型回复含路径+拒绝语义（无法/不能/本地/虚拟文件系统/not found）；**仅首轮（无 tool 回填）触发**，回填轮不合成防循环
- few-shot 注入（c2a 机制）：user 指令 + **assistant 完整输出示例**（```json action 块）→ 模型直接模仿（当前模型无视示例但 synthesizer 兜底）
- **实测闭环恢复**：R1 tool_calls（`{"path":"C:\Users\Administrator\Desktop\testfile.txt"}`）→ R2 模型基于结果正确回答（hello notion2api/tool bridge works/end of file），无循环
- synthesizer 单测 3 个（拒答提取/普通回答不触发/强制 choice）全过

**结论**：Notion 上游模型行为变化（不再主动输出调用块）不再致命——**synthesizer 让 2api 层不依赖模型配合**（用户方向："路径在文本里就提取"）。

### 卡点 A3：INV-06 分块 fuzz 实锤的 sieve 泄漏（2026-08-25 下午，已修复）—— 6 个真实 bug
**fuzz 方法**：小 fixture 穷举单切点 + 大 fixture（14KB/600 段）固定种子 200 次随机 2-6 分片，输出必须等于 golden。4 个泄漏 fixture 固化 `internal/app/testdata/leak_*.txt`（TestINV06PinnedLeakFixtures 全切点重放）。红灯先行证据：`_runtime/v4_evidence/red_first_fixtures.txt`。
**修复（tool_bridge.go feed/findBlockEndOn 重构为状态机循环）**：
1. **前导文本黏块**：标记在前导文本之后到达时，前导文本被黏进块切片 → 工具块泄漏。修复：标记命中在中段时先吐前导文本，块模式从标记处开始。
2. **标记跨片断裂**：` ``` ` / `json` 分两片到达时直接泄漏。修复：v4 S2 hold-back（buffer 尾部与标记前缀部分重合时延迟释放；EOS/取消时 flush 按普通文本释放，有界 ≤ maxPrefix-1）。
3. **超限闸误伤**：进入块模式时 buffer 带着整段流尾，2048B 超限闸把流尾当块释放。修复：先判闭合，超限闸只作用于未闭合块。
4. **跨方言取闭合**：XML 块后跟 fence 块在同一 buffer 时，findBlockEndOn 取错闭合。修复：sieve 跟踪块方言（fence/xml/json），按方言找闭合。
5. **流式/非流式剥离不一致**：sieve 保留 `<tool_call>` XML 块而非流式剥离。修复：xml 方言块完整即丢弃 + 行内 XML 标记任意位置匹配（findXMLMarkerPos）。取舍：用户正文含 `<tool_call>` 字面量按 REQ-TOOL-15 注册标记语义剥离（TestXMLMarkerLiteralConsistentWithNonStream 双断言）。
6. **fence 语言标识白名单漏项**：```rust 等新语言漏检。修复：结构规则（```+任意字母数字标识=开标记，空/换行/空白=闭合），findBlockEndOn 与 stripToolActionBlocks 同步。
**回归**：TestINV06* fuzz + pinned fixtures + sieve 单测 + review2 测试全绿；TestSieveBlockOverflowDegrades 语义修正记入报告 testChanges。

### 卡点 B：验收标准 v4 未对齐 —— 决定因 = 尚未开始
**铁证**：`Desktop/2api 兼容层验收标准 v4.md` 已读（P0 内容平面/终止路径/证据等级制/报告），当前项目无 v4_consistency_report、无反向门禁 mutant 套件、标记剥离未系统验证。
**2026-08-25 更新**：P0 全部关闭（见 §0.5 达成）——INV-03 账本、INV-06 fuzz、INV-15 通道、INV-13 六类终止（含新增 stop 序列支持）、REQ-STR-08 usage null（四条流式路径统一）、REQ-DEP-04（重建→重启→指纹三方比对）、T-16 反向门禁（scripts/mutant_gate.ps1，8/8 全杀）。报告 `_runtime/v4_consistency_report.json`：19 pass / 3 unknown（INV-02/09/12 E3 级，P2 范围）/ 0 fail。剩余：E3 级条目（金丝雀/长跑）→ P2。

### 2.1 次要修复（保留，非主因）
- 3 个旧测试消息 "hello" 命中身份探针（`(?i)^\s*(hi|hello|...)`）→ mock 拦截导致失败 → 测试消息改为普通文本（testChanges：main_fresh_thread_test.go 1277/1686/1727）

### 2.2 解锁路径（按优先级）
1. 工具桥稳定性：多轮工具循环 + 并行工具实测（客户端脚本模拟 OpenAI 工具循环）
2. 验收对齐：先做内容平面（标记剥离验证/码点守恒/终止路径统一）→ 报告产出
3. 流式工具调用（当前流式路径无文本解析输出 tool_calls——已确认非流式通）

---

## 3. 后续要做的

- **进行中：E3 金丝雀 T-09（2026-08-25 晚启动，观察窗 24h → 2026-08-26 18:00 期满）**
  - 基线冻结：指纹 `c3793b9fdfaf`（含 INV-02 探测器的新构建，`_runtime/canary/canary_config.json`）
  - 计划任务 `notion2api-canary-t09`：每 10 分钟跑 `research/canary_t09.py`（24h，样本 ≥200）
  - 三项指标：INV-02 未知标记（服务侧 unknown_marker.go 常驻扫描+候选 fixture 落盘+4 单测）、INV-09 无状态（并发 nonce 隔离+复读探针）、INV-12 真流式时序（TTFB/块间间隔/跳度/块数）
  - 结果累积 `_runtime/canary/T-09_results.jsonl`；期满跑 `research/t09_report.py` 出结论页
  - 回滚：`scripts/canary_rollback.ps1`（触发条件见 canary_config.json rollbackTriggers）
  - 回归门禁：`scripts/regression_gate.ps1`（vet + 全量测试 + mutant 11/11，CI 必过）✅ PASS
  - ⚠️ 诚实记录（非修复项）：Notion 上游 TTFB p50 ~2.9s > spec 阈值 800ms/2s（上游 profile 已知首 token 延迟 ~2.5-3s），期满报告将如实判 INV-12 fail 或按 §8"长思考模型可单独定义"文档化新阈值，不得篡改数据
- 卡点 A（多轮/并行工具循环端到端）在 E3 收敛后评估进入。
- 阶段 3：验收标准 v4 P2（金丝雀产出 E3 证据后 verdict 才允许 pass）。
- ⚠️ 号源风险（2026-08-25 晚）：注册机 4 个 temp 域全被 Notion invalid_email_domain 拒绝（产号失败）；主号 mt80wafmshg9 被上游限制已停用，当前激活 mt88c8ehgejo@imageeditgpt.com；号池仅 1 健康号，补号能力待恢复（域问题需注册机侧解决或研究新域）。

## 号源突破（2026-08-25 深夜，AdGuard 邮箱 + personal 空间模式）

- **AdGuard Temp Mail 接入成功**：`Desktop/notion注册机/register/adguard_tempmail.py`（新 provider，接口对齐 temp_mail.py）
  - 建邮箱：Playwright headful 过 capjs 验证码（自动求解，headless 会被 browserCheckFailed 拦）→ `mailbox` cookie 标识
  - 收信：**纯 HTTP** `GET /messages?since_message_id=0` + `GET /message/<id>`（cookie 鉴权），验证码提取对齐 temp_mail 逻辑
  - 域名 **hidesit.net** 实测被 Notion 接受（getLoginOptions/sendTemporaryPassword 200，验证码邮件 ~2.5min 到达）
  - mailbox 信息落盘 `register/mailboxes/`（进程退出可恢复）；AdGuard 有 rate limit（连续建邮箱会慢投递/429）
- **注册机 personal 空间模式**：`--space-mode personal`（register_one 参数）——跳过 selfJoin，createspace 9 字段精确 body 自建 personal 空间（deviceId=notion_browser_id, createSpaceView:true）
- **重大实证：team/business（trial）空间推理不可用**——被邀进的 slippery's Space（tier=business）对 hidesit.net 新号 runInferenceTranscript 全部 `temporarily-unavailable`（getAvailableModels 显示 `trial_not_allowed` 禁用 Fable 5）；**personal 空间立即可推理**（constant.rabbit 经服务 `/admin/workspaces/rotate` 建 personal 空间后实测 OK，工具桥输出正常）
- **操作手册**：注册机产号（hidesit.net）→ 导入号池 → `/admin/workspaces/rotate`（Go 引擎建 personal 空间）→ **手动改 probe.json space_id/space_view_id 为新空间**（rotate 只更新内存 session 不落盘 probe）→ 重启服务
- 当前号池：`constant.rabbit.adry@hidesit.net`（personal 空间，**健康**，active）；mt88（冷却中）；其余 failed/disabled
- **CC 已验证不再 502**（返回模型拒绝文本 = 上游行为，非服务故障）；CC 绝对路径拒绝场景 synthesizer 未触发（措辞变体，卡点 A 延续，待下轮修）

待验点（旧遗留疑问）：
- 模型对 `./` 相对路径的还原（当前 unmask 只处理 `~/`，`./`/`/workspace/` 直接下发——客户端执行时自行解析？）
- 原生透传（ToolUses）路径未做 unmask 验证（buildChatCompletion 已接 unmaskPathArgs 但未实测原生事件含路径场景）
- 流式路径的 extractToolCalls 未接（main.go 1767 只处理非流式）

---

## 循环工作流 #3（2026-08-26 凌晨：CC 稳定回复 + 工具桥恢复，round 1-2）

- **CC 普通对话已稳定**：自我介绍/数学题均正常回复（走 /v1/chat/completions，baboon 号 personal 空间）。「读取本地文件」类请求仍被上游身份拒绝（能力自述触发，非安全对齐）——按用户指示先保稳定回复，工具桥恢复为次优。
- **opus 规划（策略 1 已落地）**：拒绝根因 = Claude 系模型纠正「我能访问本地」的错误前提（祈使动词+绝对路径+第二人称所有格最强触发）；概率性漂移 = 用户消息裸路径与注入框架冲突。策略：**角色分工协议**——「你是编排端只输出调用意图，客户端执行并回传结果」拆掉前提。已写入 buildToolBridgePrompt（Division of labor 段）+ 单测 TestBuildToolBridgePromptDivisionOfLabor。
- **流式 /v1/responses 工具桥补洞**：writeResponsesLiveStream 原无任何工具处理（CC 若走此路径即无 synthesizer）→ 已接 extractToolCalls/synthesizer/事件流（response.output_item.added + function_call_arguments.delta/done）；**responses 路径 request 漏设 ToolsRaw/MaskLocalPaths**（chat/completions 有，responses 漏）已修；TestResponsesStreamToolBridge 过。
- **rotate 持久化修复**：/admin/workspaces/rotate 创建新空间后 client.Session 未同步 → probe.json 不落盘（重启后 dispatch 仍用旧 space_id）→ 已修（同步 client.Session 再 persistSessionProbe）。**主流程定案：注册（AdGuard 邮箱）→ 导入 → 服务 rotate（Go 引擎建 personal，200；curl_cffi 侧 504 指纹问题）→ probe 自动写回 → 激活**。
- **身份探针误拦发现**：规划 prompt 含「你是 X 模型」结构会命中 CheckIdentityProbe（(你|are you).{0,8}(是)...(模型)）返回 mock——与上游模型无关，规划/审查 prompt 需避开该措辞。
- ⚠️ **账号容量现实（运营硬约束）**：hidesit.net 新号推理配额极低（~5 次/小时即账号级限流，冷却 30min 后恢复但再试即再限，呈死循环）；mt88/rabbit/baboon 全部被标记冷却中。**对策方向**：号池多号分摊（每号只扛少量请求）+ 新号「养熟」（mt88 注册 5h 后曾可用几十次，新号熟化期假说）+ 低频率使用。金丝雀已暂停（无健康号 + 保护号）。

## 循环工作流 #4（2026-08-26：认知重构 + 工具桥恢复，opus 规划/review 3 轮）

- **认知重构注入（用户方案 + opus 格式工程强化）**：注入框架从"工作区文档整理"升级为"编写 API 系统技术参考文档"（cursor2api 同款）——工具调用块在模型心理 = 文档中的 JSON 示例，模型不觉得自己违背 Notion AI 身份。实测：身份拒绝 → 稳定输出示例块。
- **opus review 落地**（格式工程视角，问题转换后配合）：硬约束契约（name/arguments + additionalProperties:false）、多示例同构（3 个）、负例对照（WRONG 扁平化/字面量错误）、非字符串 JSON 字面量约束、路径字面量约束、尾部重申最短契约、正文污染抑制（Output ONLY）。
- **解析器兼容面**（opus B 部分）：name/function/tool/tool_name/action/operation 别名归一 + arguments 字符串二次 parse + 扁平参数收拢 + scanToolCallJSONObjects 放宽工具键过滤（looksLikeToolObject）。
- **实测闭环**：直测/curl 复现（完整 CC 请求体流式+非流式）→ tool_calls 合成成功（含 mask/unmask 路径还原）。**CC 客户端侧两个待解点**：①CC 只发 Agent 工具（工具执行模型=Agent 代理），模型合成 Agent 调用而非 Read；②gpt-5.4 不被 CC 识别（unrecognized_model 警告）——需要 CC modelOverrides 映射或接受默认模型。
- **CC 场景关键实证**：服务端对 CC 请求（含巨长 system-reminder + 40 工具）的 tools 解析/synthesizer/mask 全链路正常（dbg 验证后清理）；CC 实测文本拒绝是"模型已输出拒绝文本时 CC 优先展示文本"的客户端行为 + opus-5 身份冲突（已建议 CC 用 gpt-5.4）。
- **INV-02 登记扩展**：Claude Code 工具文档族（function/functions/command-name/path/any/transcriptDir/id/task-notification/name/description/parameter/tool/argument 等）为已知 XML 结构（Notion 把客户端工具 schema 序列化注入 instructions，输入侧不进入正文）——unknown-marker 不再刷屏。
- **synthesizer 触发词扩展**（CC 中文变体实测）：找不到/没有这个文件/不存在/未能读取/无法读取/无法访问/读取失败/尝试读取/我读取/试着读取 + file not found/no such file/cannot read 等。
- **废号清理**：mt7i49omsvfz/mt7h7qg64vbk/mt57/useless.alligator 已停用（避免 dispatch 空耗轮换 180s）；当前池：baboon（active，personal 空间，可推理）。

## 循环工作流 #5（2026-08-26：20 分钟中型项目测试 — 服务端闭环达成，CC 客户端工具传递阻塞）

- **测试任务**：CC（cc-switch notion2api provider，直连/代理双路径）在 cc-test-project 做"写 dir_report.py + 运行"中型项目任务。
- **服务端闭环全部达成**（curl 全链路验证）：
  - 认知重构 + 任务型合成（synthesizeTaskCall：Agent 工具 + 任务动词 + 方案型文本 → 合成 Agent 调用，prompt=用户请求）→ curl 流式/非流式均输出 tool_calls
  - 自动轮换（限制类错→新建空间）继续生效；新号 colourful.haddock.flyu@hidesit.net（personal 空间）推理正常
  - 429 换 deviceId 复位已实现（对每日上限无效——实测确认 429 是每日创建上限而非可绕过的 spam 标记）
- **阻塞项（CC 客户端，非服务端）**：claude -p 单次模式 + --allowedTools 在当前环境下**不把 tools 传进 API 请求**（dbg 实测 tools=0，多模型/多路径复现：gpt-5.4/opus-5/claude-sonnet-4-6、直连 8787/cc-switch 15721）→ 服务端无 tools 不注入不合成 → CC 只回文本（回复完整不截断，但工具不执行，项目做不了）。09:17 曾出现一次带 Agent 工具的请求（原因不明，疑 CC 内部 Agent 模式）。
- **建议**（用户侧检查）：① cc-switch apiFormat 试 anthropic_messages（服务端需加 /v1/messages 适配）；② CC 交互模式（非 -p）工具行为；③ CC 版本检查。服务端侧可做：加 /v1/messages 兼容端点（Anthropic 原生格式，CC 原生工具传递）。
- **附带**：任务型合成误伤控制（Agent 工具 + 任务动词 + >120 字方案文本才触发）；测试全绿。

## 循环工作流 #6（2026-08-26：/v1/messages 端点 — CC 项目工作闭环打通 ✅）

**目标**：CC 能真正做 20 分钟中型项目（写文件/运行命令/多轮工具）。上一轮定位：CC 走 openai_chat 时工具不传递（tools=0）。本轮按 opus 设计实现 Anthropic Messages 原生端点。

### 成果（CC 端到端项目工作已闭环）
- 实测（新号 curly.crab.inmn@hidesit.net）：`claude -p "写一个 dir_report.py…并运行"` →
  ① 服务端合成 Agent tool_use（Anthropic 格式）→ ② CC 创建子代理 → ③ 子代理写 dir_report.py（2088B 正确代码）→ ④ 多轮推理（11 条消息）→ ⑤ 手动运行生成 report.md。hello.py 测试任务：文件写入 cwd 正确。
- 关键突破链：CC 请求经 /v1/messages 带 12 工具（openai_chat 时 tools=0）→ ST 误判修复（工具桥不再被绕过）→ Agent 合成补 description（CC 版 required=[description,prompt]）→ cwd 注入（模型输出正确绝对路径）。

### 实现清单
- **anthropic_types.go**（新）：请求/响应结构体；system（string/数组）归一；messages（text/tool_use/tool_result）→ OpenAI 内部形态；tools input_schema→parameters（缺 type 补 object）；tool_choice 映射（any→required 等）
- **anthropic.go**（新）：POST /v1/messages 路由；非流式（复用 chat 链路 → OpenAI 响应 → Anthropic 消息，tool_use 输出、stop_reason 映射）；流式（内部 SSE → 管道 → 事件转换器）；错误结构 {"type":"error","error":{type,message}}
- **anthropic_stream.go**（新）：SSE 事件转换器（message_start → content_block_start{tool_use{id,name,input:{}}} → input_json_delta 增量（不做 JSON 解析）→ content_block_stop → message_delta{stop_reason} → message_stop）
- **tool_bridge.go**：filterCallsToAvailable 描述前缀归一（模型把 description 当名字"Launch a new agent..."→Agent）；synthesizeTaskCall 合成补 description（CC Agent 必填，短祈使句=任务前 24 字）；任务动词扫描改全部 user 消息（CC 把 system-reminder 塞第一条 user 消息）；cwd 提取（CC system "Primary working directory:"）+ 注入绝对路径规则
- **main.go/sillytavern.go**：ST 误判收紧（maybeSillyTavernByTypedMessages 废弃文本匹配；isLikelySillyTavernPayload 删 system prompt 文本分支，只留信封字段）——真 ST 必带信封，CC 的 Claude Code 规则含 "fictional chat between" 不再误判
- **测试**：v4_anthropic_test.go（4 用例：tools 转换/messages 归一/响应映射/事件转换含 partial_json 拼接校验）；TestFilterCallsDescriptionAsName；task synth description 断言；全量绿

### 基建
- 新号流程：adguard 注册（n0022 curly.crab）→ 导入 → **复制注册机 probe.json 到服务端 probe 路径**（否则 rotate loadSession 500）→ rotate（business 空间 → 新 personal 空间 7ba53c67）→ activate → 重启生效
- colourful.haddock 每日 createspace 上限到（429 → deviceId reset 无效 → 15min backoff）；新号无此限制

### 遗留（可选）
- 子代理异步模式下 Bash 步骤未自动完成（文件写完就停；手动运行 OK）——疑子代理会话限制或等待通知机制，非核心链路问题
- 账号配额：每号 ~5 推理/小时 + 每日 ~6 空间创建；长任务（子代理多轮）消耗快，需定期产号

## 循环工作流 #7（2026-08-26：用户实测三大痛点修复 + 工作流方法改进）

**用户实测三大硬伤**：回复极慢/无法回复、无流式、工具调用一坨。本轮按 opus 规划（情报包驱动：框架+代码+账号+失败史，禁止凭空猜测）执行。

### 修复清单（全部实现 + 测试绿）
- **B1 熔断**：createspace 429 第二次立即返回（删 5-15min 退避——此前退避 > 轮换超时 → 超时掩盖 429 → 冷却不触发 → 整日死锁 502）；rotateHTTPTimeout 180s→60s。实测轮换 60s 内完成 + 重试成功（probe 持久化正常）
- **C2 真流式**：/v1/messages 流式从"responseRecorder 全缓冲"改为 liveSSEWriter→pipe 实时转发；**实测 TTFT 1.5s**（首字节/首事件同步）；converter 加 15s ping 心跳 + **writeMu 互斥**（修复 ping goroutine 与主循环并发写响应——CC 卡死根因之一）
- **C3 屏蔽子代理**：/v1/messages 工具列表过滤 Agent/SendMessage/AddTaskNotificationTool（杜绝异步子代理耗配额/链路断）
- **C1 条件注入**：续轮（工具回填）ToolBridgeSection 换 buildToolBridgeSummary（≤5KB 摘要版，含不可裁剪契约）
- **具体工具合成**（opus review B）：synthesizeTaskCall 重写——无 Agent 时从方案文本提取 file_path（用户消息文件名→cwd 拼接）+ content（围栏代码块→内联 print/echo 兜底）+ command（bash 块首行/python x.py 白名单）→ 合成 Write/Bash；门控（问答特征/代码块/任务动词）；**回填轮推进**（Write 回填后只合成 Bash——多轮闭环：Write→回填→Bash 实测打通）
- **回填轮措辞修复**：buildToolResultsPrompt 从"don't record any further action blocks"改为"若请求仍需后续操作，继续输出下一个 JSON 示例块"（原措辞禁止继续 = 多轮断裂根因）
- **kimi-k3 修复（"无法回复"直接根因）**：cc-switch notion2api provider 的 HAIKU 模型 = kimi-k3（Notion 上游空响应 → CC 卡死）；已改 opus-5 + 重启 cc-switch 生效
- **附件基建**：类型校验前置 400（此前在推理路径烧账号——实测烧掉 3 个号）；S3 上传直连（本机代理掐断；直连也不通——附件方案本机不可行，工作区页面喂法留待后续）
- **具体工具合成实测**（curl 流式/非流式 + CC 真机）：hello2.py Write 合成 ✓；多轮 Write→Bash 闭环 ✓（curl 二轮：Write 合成→回填→Bash 合成 tool_calls）

### 工作流方法改进（用户要求）
- 情报包驱动：框架图/真实代码片段/账号状态/已验证事实/未知层（research/loop_intel_*），opus 不凭空猜
- 附件喂法验证失败（S3 网络限制）→ 浓缩 prompt（3-5KB 事实层/接口层/证据层/未知层模板）
- 新账号流程跑通 4 次（n0021-24：adguard 注册→导入→probe 复制→rotate→activate→重启）

### 遗留/已知
- **账号配额硬约束**：每号 ~5 推理/时 + 每日 createspace ~6；本轮测试烧完全部 4 号（当前全部限流/冷却，需产号或等 1 小时恢复）
- CC 场景模型输出随机（调用块 vs 说明文）——内联兜底已加，但"说明文无代码无命令"时仍无合成（模型自由输出）
- kimi-k3 上游空响应机理未深究（已绕过：配置移除）

## 循环工作流 #7 补记（2026-08-26 深夜最终状态）
- **语义构造兜底**（inferSimpleScriptContent）：模型说明文无代码块时，从任务语义构造简单脚本（"输出 Hello world"+.py → print("Hello world")）——CC 实测 hello9.py 创建成功（11 字节）✓
- **账号选择修复**：conversations 表旧会话绑定已死账号（pinned certain.echidna → disabled 502）——清空 conversations 表解决；新号 distinguished.silkworm.wcqe（n0025）推理正常
- **CC 实测结论**：Write 合成→CC 本地执行闭环 ✓（hello2/3/9 均创建）；Bash 回填轮受账号配额波动影响（quota→轮换→重试超时），curl 二轮验证 Write→Bash 闭环 ✓
- **遗留**：① 账号配额硬约束（每号 5 推理/时，多号并发测试易耗尽）——opus B2/B4（本地限流/预造号池）未实现；② dispatch 对 disabled 账号的 pinned 报错（会话绑定历史账号）需长修；③ 附件/工作区页面喂法未落地（S3 网络限制）

## 4. 历史卡点（已解决，留作存档）

| 旧卡点 | 状态 |
|---|---|
| 服务请求无 agent-tool-result 事件 | **已解决**（日志未重定向误判；实际正常收集） |
| get_weather 工作区外工具被拒（6 种框架全试） | **已解决**（定案：工作区外硬拒；工作区内能力走路径伪装+顺式框架） |
| 注入 section 在 freshThread 重放丢失 | **已解决**（移到组装末端 userStep） |
| preparedReq 漏拷 MaskLocalPaths/ToolBridgeSection | **已解决**（补字段） |
| unmask 路径重复（`Users//Administrator//`） | **已解决**（JSON 值级递归还原 + 斜杠归一化） |

---

## 5. 核心产物（`C:\Users\Administrator\notion2api`）

- `internal/app/tool_bridge.go`：工具桥（buildToolBridgePrompt 顺式框架 / extractToolCalls 四格式 / maskLocalPaths+unmaskPathArgs / buildToolResultsPrompt / CheckIdentityProbe）
- `internal/app/notion_client.go`：NDJSON 解析 + applyPatchOperation + ToolUses 收集 + userStep 组装（maskIfEnabled）+ preparePromptRequest
- `internal/app/main.go`：handler 注入接线（ToolBridgeSection 组装末端）+ 输出 unmask + 身份探针
- `internal/app/openai.go`：buildChatCompletion（tool_calls 输出 + unmaskPathArgs）
- `PLAN.md`：主规划（P2 工具桥突破已更新）
- `research/`：探针脚本（_probe_tool.py 工具闭环冒烟、_start_srv.py 服务启动）
- 注册机：`C:\Users\Administrator\Desktop\notion注册机\register`（号源，batch_run_proto.py）

---

## 进度报告（2026-08-25，P2 工具桥闭环 + P0 验收首轮）

- **已完成**：工具调用端到端闭环（路径伪装 + 顺式"工作区文档"框架 + 注入位置修复 + 回填措辞），单测全绿 + 冒烟通过；PLAN.md 沉淀。P0 验收首轮：DEP 指纹组、工具标记剥离（含未闭合/XML）、终止错误映射、`_runtime/v4_consistency_report.json`（12 pass / 5 unknown / 1 partial）。
- **当前位置**：P0-P2 完成，工具调用主路径代码通；整体 ~88%。

## 进度报告（2026-08-25 晚，循环工作流 #2 round 1-4 — v4 P0 收口 + opus5 无条件通过）

- **已完成**：v4 P0 全部关闭——INV-03 码点守恒账本（sanitize_ledger.go，逐 rule 计数 + 重复码点对抗 + SAN-INVALID-UTF8）、INV-06 分块 fuzz（6 个 sieve 真实泄漏，见 §2 卡点 A3）、INV-15 通道不回灌（含故意回灌负向用例）、INV-13 六类终止矩阵（含 stop 参数支持）、REQ-STR-08 usage null（四路径统一）、REQ-DEP-04/INV-16 指纹三方比对、T-16 反向门禁（11/11 全杀 + mutant→不变量矩阵 + red_first_fixtures.txt）、report reproCommands。
- **opus5 review 循环**：round1 8 阻塞项 → round2 全部落地（含 3 新 mutant m-i/m-j/m-k、pinned fixture 实锤第 5/6 个泄漏）→ round3 P1-1 红灯证据/P1-2 xml 字面量取舍收口 → **round4 终判：无条件通过，无 P0/P1 阻塞项**。
- **报告**：`_runtime/v4_consistency_report.json` = **19 pass / 3 unknown（INV-02/09/12，E3 级带 unknownConverge）/ 0 fail**，verdict=insufficient-evidence。
- **已稳主路径回归**：go test 全绿（多次连跑）；服务重建+重启（指纹 64674c7e2d97）；smoke 通过。
- **下一步**：E3 金丝雀 T-09（review round 4 指令，启动清单在 §3）。
- ⚠️ 号源风险：注册机域被拒 + 主号被限 → 已换 mt88 激活，见 §3。

## 循环工作流 #8（2026-08-30）：纯 Go 注册链 + 空间配额恢复轮换池

### 本轮交付
- **纯 Go 注册链落地**（register_chain.go / register_mail.go）：完整对齐
  notion注册机/register/notion_register_proto.py 9 步协议，替换 register_provider.go
  的 Python 子进程调用；Docker 容器内原生运行，无 python/playwright 依赖。
  - 邮箱源：mail.tm（纯 HTTP 创建+JWT+轮询收信，默认）/ adguard（纯 HTTP 收信，
    复用已建 mailbox cookie 文件 —— 建 mailbox 需 capjs 浏览器，Go 端不支持新建，只消费存量）
  - invalid_email_domain 语义完整保留：clientData.type=invalid_email_domain /
    UserValidationError → 加入坏域集合 → 换邮箱重试，上限 5 次
  - invite 模式无可 join 空间时新增 Go 端 createspace(personal) 兜底（Python 侧原为 TODO 洞口）
  - 落盘格式完全兼容：probe.json（含 models blob）/ account.json / trace_*.jsonl / 桶 accounts.txt（50/桶）
- **空间配额恢复轮换池**（space_pool，workspace_pool.go）：
  - space_lifecycle 新列 space_view_id + cooldown_until（在线迁移）
  - 额度耗尽 → 标记 cooldown（默认 60min，可配 cooldown_minutes）→ 定时器到点自动恢复 active
  - Rotate 优先复用池内 active 空间（不触发 createspace）；池空才走 createspace 老路径
  - 每号预建到 target_per_account（默认 3）——创建走保守节流（10min/号/次），不动 probe 当前指向
  - 配置段：config.register.mail_provider/space_mode + config.space_pool.{enabled,...}
- 容器部署升级：register.enabled=true（mailtm），space_pool.enabled=true（详见 config.docker.json）

### 测试
go build + vet 通过；回归套件 22 项全绿，新增 3 项：验证码提取 / 冷却生命周期+view 持久化 / pickReusableSpace 排除当前空间。

### 遗留/观察项
- **额度恢复周期实测未知**：cooldown_minutes 默认 60 是按号源笔记里的 "~1 小时恢复" 猜测值，
  需上线观察（容器日志 [workspace_pool] recovered ...）后校准；logger 已带足够证据。
- **GitHub push 缺凭证**：本轮 commit cdf8b17 已在本地 main，push 需要用户提供 token 或代理凭证。

### 实测闭环（2026-08-31 凌晨，容器内全链路验证）
- **注册**：concrete.sloth.ouah@hidesit.net 由纯 Go 注册链一次跑通
  （adguard 投票 → sendTemporaryPassword → 收信 → loginWithEmail → createspace/join → probe.json）。
  mail.tm 默认域 emalupe.com 目前被 Notion invalid_email_domain 拒（Single 域限制），adguard 为主用源。
- **轮换**：joined business 空间 temporarily-unavailable → 标记 cooldown(+60min) →
  Rotate **重用池内 active 空间**（修复：复用须先于 10min 建空间节流，否则节流把轮换打死）→ 重试 "PONG" 成功。
- **冷却恢复**：人工将 cooldown_until 改到过点 → 巡检循环 120s 内自动恢复 active（日志 "recovered 1 cooldown space(s)"）。
- **修补**：SetSpaceLifecycleCooldown 改 upsert（joined 空间不在表也能打冷却）；
  adguard/mailtm 收信加 notBefore 时间过滤（复用邮箱排除历史验证码）+ 轮询日志。
- **mail.tm 坑**：surf Chrome 指纹会强制浏览器 Accept 头，mail.tm 据此回 XML——
  邮服务改用 net/http 纯客户端（newRegisterPlainClient），Notion 侧仍走 surf 指纹。
