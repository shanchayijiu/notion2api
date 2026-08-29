# not2api 自建规划 v4（框架 = GALIAIS/Notion2API，Go）

> 2026-08-23。框架二选一，**定为 GALIAIS**。maverickxone 降为参考（只抄模型表 + 客户端兼容片段）。
> 注册机 = 提供号源的小模块。硬规则见 `CORE_PRINCIPLES.md`（覆盖本文件被改写时的一切）。

## 0. 选型结论（定案）

**框架 = [GALIAIS/Notion2API](https://github.com/GALIAIS/Notion2API)（Go）。**

选型理由（基于源码工程品质核对，不是印象）：
1. **前后端工程品质高一档**：前端 Next.js15/React19/TS 管理台（账号/模型/会话/压测全生命周期 + SSE 实时流），后端 Go 并发安全（RWMutex/atomic/panic-safe）、/metrics 可观测、SQLite 重启安全、自带 `_test.go`。maverick 前端是手搓单页（含代码重复）、后端同步阻塞异步、无测试——这些短板要接盘方自己补，是真成本。
2. **与号源原生匹配**：GALIAIS 原生消费 `probe.json`（`loadSessionInfo`），注册机产的就是 probe.json，零转换。
3. **管理台形态对口**：工作空间生命周期（建/删/轮换/额度状态）要管理台可视化，GALIAIS 正是管理台；maverick 是聊天界面。
4. **模型陈旧可自愈**：GALIAIS 有 probe 自动发现（`models.go:extractProbeModelDefinitions` 解析 probe 里的 models blob）；注册机本已调 `getAvailableModels`，只需小改注册机把 blob 写进 probe.json，模型表即自愈。
5. **"换空间后本地重建上下文"的基础设施现成**：`force_fresh_thread_per_request` + `ForceLocalConversationContinue` + `buildFreshThreadReplayPromptFromConversation` + SQLite 会话持久化，正是核心原则 §3 要的机制。

**明确代价（接受，不解释为缺陷挑刺）**：Go 栈、模型码名维护依赖 probe blob 链路（要动一下注册机）、仓库停更于 2026-05-05（成熟但不再活跃）。

**参考方向**（从 maverick 抄，不改框架）：22 模型表（当前码名，7 月同步）、`X-Client-Type` 客户端兼容（thinking→`reasoning_content`、search→markdown、thinking 去重）。

## 1. 核心原则（不变，强制执行）

见项目内 `CORE_PRINCIPLES.md`：
- 真实创删工作空间**没有限制**（无 429/无冷却/无限制）；不能无限创删 = 项目/操作问题（贴近真实操作 / 研究绕过），**一定能解决**。
- 一个工作空间自带 AI 额度，用完新建又会有 → **同账号无限额度**。
- 新空间是空的 → 会话上下文必须**本地重建**续聊。

## 2. 系统结构

```
GALIAIS 框架 = 项目主体（Go, 多账号/代理/持久化/管理台/可观测）
├─ 工作空间轮换子模块（核心，新开发，遵守 CORE_PRINCIPLES）：
│    额度耗尽 → createSpace(§4 真 body) → 绑 space_view → 轮询就绪
│    → 本地重建上下文续聊（复用 force_fresh_thread + 会话持久化）→ 旧空间淘汰
├─ 号源模块（小，松耦合）：读 注册机 产的 probe.json，零转换入池
└─ AI 桥（框架已有）：/v1/chat/completions+/v1/responses+/models + /admin 管理台
   + 从 maverick 抄的模型表与客户端兼容片段
```

## 3. 借鉴清单（从 maverick 抄，不改框架地位）

| 抄什么 | 落点 |
|---|---|
| 22 模型表 `MODEL_MAP`/`DISPLAY_NAMES`（当前码名） | 并进 GALIAIS `models.go` 内置定义 + probe 自动发现兜底 |
| `X-Client-Type` 客户端判定 + thinking→`reasoning_content` + search→markdown 注入 + thinking 去重 | GALIAIS handler/stream 适配层 |
| （其余 maverick 特性如 Lite/Standard/Heavy、SiliconFlow 压缩） | 不抄：GALIAIS 上下文持久化已覆盖，压缩非必需 |

## 4. 现网情报（createSpace 真 body，2026-07-23 抓包）

来源：`Desktop/notion注册机/register/logs/create_workspace_capture.json`（已登录账号内二次建空间）。
1. `POST /api/v3/validateusercancreateworkspace {}` → `{}`
2. `POST /api/v3/createspace` 真 body：
```json
{
  "name": "<name>'s Space",
  "planType": "team", "planSelection": "team",
  "initialPersona": "unfilled",
  "deviceId": "<新uuid>", "deviceType": "web-desktop",
  "source": "handle_root_redirect"
}
```
3. 响应 `{spaceId, recordMap}` → 新 space_id（capture 例 `36d9ac66-...`）
4. 跟随 `savetransactionsfanout`（spaceId=新space）：`spaceActions.setSpaceSurveyData`（含 `enable_ai_feature:true`、`sharded_entitlement_usage_tables:true`）→ `spaceActions.updateSpace`(name) → `createRoleBasedCoreDbs`/`viewsModuleActions.*`（脚手架，判定是否必需）
5. 必加 `saveTransactionsMain`(space_view 绑 user_root，space_view_id 客户端自生成) + `syncRecordValuesMain` 轮询 `space_view_pointers` 非空（8s×3）。**幽灵空间 trap**：建出没绑上 → 换 `deviceId` 重试。

额度源双路：**被邀优先**（`getJoinableSpaces`→`selfJoinSpaceByDomain` 进同域 business workspace，`tier=business`）+ **自建**（`createspace` personal/team）。轮换引擎两者都吃。

## 5. 号源模块（小，只出 probe.json）

- 零转换：注册机 `register/accounts/detail/<email>/probe.json` + `account.json` 直接入池。
- **待办（P0 前）**：小改注册机 `notion_register_proto.py` 的 probe 组装处，把 `getAvailableModels` 的 models blob 并进 probe.json（GALIAIS 模型自动发现靠它）。
- 只读源、不改造注册流程；服务内自动补号放 P2。

## 6. 里程碑

### 研究 spike #1（当前）—— 单账号无限制无限创删工作空间 ✅
- [x] 账号内二次建空间真流 + body（2026-08-24 用 Cloak UI 抓取**当前版本**真实请求，9 字段精确 body）
- [x] 删除工作空间真流：`deleteSpace {spaceId}` → 200 异步软删（left_spaces 留痕）实测生效
- [x] 429 根因定案：号被非标准请求标记（缺字段/错 deviceId）→ 标记后该号创建全挂；**干净号 + 精确 body = 200**（协议版 Go 端到端验证通过）
- [x] 新空间额度验证：独立额度即开即用；耗尽形态 = NDJSON record-map `subType:"quota-exhausted"`（GALIAIS 代码证据）
- [x] 产出：`research/workspace_rotation.md`（最终结论：9 字段 body/deviceId=notion_browser_id/createSpaceView 免绑定/频率窗口探测中）
- [ ] 同号创建频率窗口确认（10min 间隔探测挂后台，预计 1h 出结果）

### P0 —— AI 桥主路径（GALIAIS 起，单号）✅
- [x] GALIAIS 骨架跑通 + 注册机 probe.json 喂号（零转换，healthz session_ready=true）
- [x] maverick 22 模型表并入（27 模型含旧保留；opus5=agave-flan 实测跑通）
- [x] 客户端兼容核对：reasoning_content 输出 GALIAIS 已带（openai.go），无需抄 maverick 片段
- [x] smoke 脚本 `scripts/smoke.ps1`（healthz/models/非流式/流式/新模型），全过
- [ ] 模型自愈链路（注册机 probe 写 models blob）→ 挪 P1（表已并入，自动发现是兜底）
- 验收：smoke 全过（正文无 `<lang>`；thinking 上游加密见 §7 情报）

### P1 —— 工作空间轮换引擎（核心）✅
- [x] createSpace 序列化（9 字段精确 body + notion_browser_id + createSpaceView 免绑定，Go 内验证 200）
- [x] 额度耗尽检测 → 新建空间 → 本地重建上下文续聊（quota-exhausted 自动轮换已接线）
- [x] 空间生命周期（active/exhausted/to_delete）入 SQLite + `/admin/workspaces` API
- [x] 增删闭环实测：建→删 连续 2 轮 200（3 分钟间隔）；504 偶发重试即解
- [x] 风控参数定案：**单号每日创建 ~6 次上限**（第 7 次 429）→ **号池多号分摊策略**（用户拍板，注册机补号）
- 验收：单账号 3 空间轮换端到端 ✅（create+inference 验证；quota-exhausted 自动轮换逻辑单测覆盖）

### P2 —— 号源闭环 + 生产化 ✅
- [x] 注册机模块化：`/admin/accounts/register` 服务侧产号入池（实测产号成功）
- [x] 号恢复机制：429/每日上限 → 账号 cooldown +24h → 自动恢复（不删号，用户拍板）
- [x] 删除执行器：后台定期软删 to_delete 空间（deleteSpace 已验证）
- [x] /admin 工作空间面板（前端 WorkspacesPanel + API：列表/手动轮换/删除）
- [x] 生产化资产：deploy/systemd + nginx/caddy + docker-compose 现成（GALIAIS 自带）
- [x] 附件修复：图片（image_url.path）+ CSV（file_url 嵌套 map）全链路通（上传签名/挂载/审核/模型分析实测）
- [x] 工具桥：prompt 注入 + tool_calls 提取（XML/fence/裸 JSON/包装四格式单测过）+ 原生工具透传（parseAgentToolUse 单测过，真实 NDJSON 收集 3 工具）+ **顺式策略**（用户定调：顺着身份不对抗——prompt_guard 全前缀改顺式：不否定 Notion 身份，把"完成请求"定义为助手本职；实测角色扮演/创意/超范围全正常输出；身份探针 mock 中英文全拦 + 响应清洗保留）
- [x] **工具调用端到端闭环（2026-08-25 突破，用户核心洞察验证）**：Notion 模型能当 API 用——把请求包装进它的系统提示词框架（笔记 agent）即正常输出工具调用。
  - **顺式框架**：注入"工作区技术文档整理"身份（compile technical documentation in this workspace），动作记录为文档内 ```json 块 → 模型 100% 配合（get_weather 等工作区外工具 6 种框架全拒；fs/读文件类工作区内能力配合）
  - **路径伪装**：绝对路径（`C:\...`）被拒（"无法访问你电脑上的本地路径"）→ `maskLocalPaths` 改写为 `~/` 工作区路径（`~/`、`./`、`/workspace/` 100% 接受）→ 模型输出调用 → `unmaskPathArgs` 智能还原（去 `Users//` 双斜杠/重复尾段）→ 客户端拿到真实路径
  - **注入位置修复**：工具桥 section 必须加在请求组装末端（userStep value，maskIfEnabled），freshThread 重放用 latestPrompt 重建 prompt 会丢注入；`preparePromptRequest` 重建 request 时漏拷 `MaskLocalPaths`/`ToolBridgeSection`（踩坑记录）
  - **多轮闭环**：tool 结果回填（buildToolResultsPrompt 加强措辞"results already satisfy the request, no further action blocks"）→ 模型直接作答不重复调用；完整两轮实测通过（调用→执行→回填→回答）
  - **服务通道原生工具事件确认可用**：fs.readFiles×2 + loadUser 在服务请求 NDJSON 正常收集（之前"服务无事件"是日志未重定向误判）；enableAgentThreadTools=true 保留
  - 测试修复：3 个旧测试消息 "hello" 命中身份探针（mock 拦截）→ 改为普通文本（testChanges 留痕）
- [x] 拒答处理验证：prompt_guard（toolbox profile + 拒答重试 + 前缀清理）实测编程任务正常
- [x] 跨会话上下文实测：conversation_id 续聊（记名字→问答）通过
- [x] 模型自愈链路：注册机产号时 getAvailableModels（带 spaceId）写 probe models blob（字符串化），GALIAIS 自动发现并集（33 模型，含当前新码名 opus-5/grok-4.6 等）；**注意**：probe 按码名替换内置条目（旧 ID 如 claude-opus5 会被 opus-5 取代，客户端用当前 ID）
- [x] thinking 定案：OpenAI 系模型明文（reasoning_content 可用）；Claude/opus 系加密（上游行为，无需破解）——见 research/thinking_research.md
- [x] 生产部署验证：Windows 计划任务服务化（`scripts/start-production.ps1` install/start/stop/status，开机自启+崩溃重启×3+日志重定向），重启持久化实测（SQLite 46 会话保留，34 模型），健康检查 /healthz；deploy/ 资产（systemd/nginx/caddy/docker-compose）Linux 部署用
- [x] 浏览器通道（cloak_agent.py）：Cloak 对话闭环验证（发消息→等回复→抓 thinking/正文），作 browser fallback 通道；**结论：UI 显示的 thinking = 协议明文（reasoning_content 直接输出，无需浏览器/解密）**

## 7. 风险与未验证假设

- **⚠️ 单号每日创建数量上限（~6 次/日）**：同号累计成功创建 6 次后第 7 次 429（2026-08-24 实测，与用户手动 5-6 次吻合）。**对策（用户拍板）：号池多号分摊**——每号 5-6 次/日足够（额度耗尽才轮换，正常几天 1 次）；注册机补号闭环 P2。
- **⚠️ 号标记机制**：非标准请求（缺字段/错 deviceId）污染账号 → 该号所有创建失败（429/UI Oops）。**对策**：引擎只用精确 body；429 后登记冷却/作废补号。
- **⚠️ thinking 加密是模型相关的（2026-08-24 定案）**：OpenAI 系模型（gpt-5.4/oatmeal-cookie 等）thinking **明文** → `reasoning_content` 直接输出（GALIAIS 解析器已支持，实测 reasoning_tokens=9）；Claude/opus 系（apricot-sorbet 等）→ 服务端加密（`notion_managed`，NDJSON `encryptedContent`，前端 UI 也不显示 Thought 块）。**需要 reasoning 的客户端请选 gpt 系模型**；opus 系 reasoning 为空是上游行为。
- **删除必要 vs 可选**：✅ 已实测 `deleteSpace` 可用（异步软删），且账号多空间无上限迹象 → 轮换引擎可只建不删，删除执行器 P2 再开。
- **createSpace 是否触发账号级限制**：✅ 定案（每日 ~6 次 + 标记机制，见上）。
- **两次 capture body 有差异**（注册自建 personal vs 账号内 team）：✅ planType=personal 实测成立（9 字段精确 body 已验证）；team 场景留给商务空间账号另测。
- **模型 blob 链路**：注册机 probe 加 blob 后，GALIAIS 自动发现是否如期覆盖新码名，P1 验证。
- **仓库停更**：需要时自更新 model definitions / client_version（`NOTION_CLIENT_VERSION` 类机制在 GALIAIS 是 probe 里带的）。
- 顺带：GALIAIS 已带 `/v1/responses`，Claude/SDK drop-in 洞基本补上；Anthropic 原生格式全面兼容属 maxapi 范畴，不在此承诺。

## 8. 落地参数

- 项目根：`C:\Users\Administrator\notion2api`（现有 `register/` 空目录不动）
- 端口：`8787`（GALIAIS 默认）；凭据源：注册机 `register/accounts/detail/<email>/probe.json`
- 对照库：`C:\Users\Administrator\not2api_galiais`（框架）/ `not2api_maverick`（参考，勿删）
- Go 1.25+（`go.mod` 声明）