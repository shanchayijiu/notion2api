# workspace_rotation.md — 工作空间轮换研究（spike #1 收口 ✅）

> 2026-08-24 最终定案。账号 mt6puecq4ubt@imageeditgpt.com（注册机新产干净号）验证全链。

## 结论（全部实测，非猜测）

1. **创建**：`POST /api/v3/createspace` 纯 HTTP 可行（Go 标准库 TLS 指纹即可，无需指纹伪造库）。
   - **当前版本精确 body（9 字段）**，源自 Cloak UI 向导抓取的真实请求：
   ```json
   {
     "name": "<name>",
     "icon": "🏠",
     "planType": "personal", "planSelection": "personal",
     "initialPersona": "unfilled",
     "deviceId": "<cookies 的 notion_browser_id>",
     "deviceType": "web-desktop",
     "source": "handle_root_redirect",
     "createSpaceView": true
   }
   ```
   - **`deviceId` 必须 = cookies 里的 `notion_browser_id`**（前端 getExperimentDeviceId 读它；account.json 的 device_id 和 cookies 的 device_id 都不是）。
   - **`createSpaceView:true` → 服务端直接建 view**，响应带 `spaceViewPointer`（无需手动 saveTransactionsMain 绑定——协议已演进，7-23 的手动绑定流程已过时）。
   - 响应：`{spaceId, spaceViewPointer:{id,table,spaceId}}`。

2. **429 根因（最终定案）**：**号被"非标准请求"标记**（缺字段/错 deviceId/多余字段的创建尝试），标记后该号**所有**创建失败（HTTP 429 + UI 向导也会 Oops）。**干净号 + 精确 body = 200**（用户手动成功 = 号干净；之前全部 429 = 在已污染号上测试）。
   - 非标准请求：7 字段（缺 createSpaceView）→ 429；8 字段（缺 icon）→ 429；16 字段（domain/emailDomains/domainType 等多余）→ 400 ValidationError；正确 9 字段 → 200。
   - **同号创建频率/数量**：3 分钟间隔可行；**单号每日累计创建 ~6 次后第 7 次 429**（2026-08-24 实测，与用户手动 5-6 次吻合）→ **号池多号分摊**（用户拍板），单号 5-6 次/日足够（额度耗尽才轮换）。
   - 被标记的号是否恢复（时间窗口）待探测；**恢复策略：注册机补新号**。

3. **额度**：新空间独立额度，刚建完即可 inference（Go 端到端实测 OK）。
   - 耗尽形态（GALIAIS 代码证据）：NDJSON record-map `subType:"quota-exhausted"`。

4. **删除**：`POST /api/v3/deleteSpace {spaceId}` → 200 异步软删（left_spaces 留痕）。**只建不删策略也可行**（实测账号多空间无上限迹象）。

## 轮换引擎操作序列（纯 HTTP，Go 内已验证端到端）

```
createspace {9 字段精确 body} → 200 + spaceId + spaceViewPointer（免绑定）
→ 更新 session.space_id/space_view_id
→ runInferenceTranscript（新空间额度即开即用）
→ quota-exhausted / 非 200 → 节流 10min → 再建 → 循环
（可选）deleteSpace 旧空间（软删，P2）
```

## 验证记录（mt6puecq4ubt / mt6rrdq5mb9r，2026-08-24）

- Cloak UI 完整向导（For personal life 流程）→ 创建成功 → 进入 "tprobe's Space" 主页 ✅
- 协议版（curl_cffi chrome142 + 9 字段 + notion_browser_id）→ 200 + spaceViewPointer ✅
- Go 轮换引擎（TestRotateProbe）→ ROTATE_OK + 新空间 inference "OK" ✅
- **增删闭环（同一活跃号）**：UI 建 1 → HTTP 建 1（200）→ 建→删→建→删 连续 2 轮全 200（3 分钟间隔）✅
- **偶发 504**：Notion 服务端慢路径（HTML 错误页），重试即解（引擎已内置非 5xx break / 5xx 重试）

## 关键风险与应对

- **号标记**：严禁在号上试非标准请求（缺字段/错 deviceId 的尝试会污染账号，之后该号所有创建失败 429/UI Oops）；429 后该号作废（注册机补号）。
- **频率**：实测 3 分钟间隔可行；轮换引擎节流保守保留 10 分钟/次。
- **504**：偶发，重试（引擎内置）。
- **协议漂移**：字段变化时用 Cloak UI 抓最新真实请求（已确立此方法：onboarding → For personal life → Continue → Skip for now → 创建）。

## 风控参数（2026-08-23 实测 + 前端 JS 静态分析）

### 429 根因（已定位，2026-08-23 前端 JS chunk 552108 静态分析 + 实测）
- **deviceId 必须 = cookies 里的 `notion_browser_id`**（前端 `getExperimentDeviceId` 读它）。
  注册机 account.json 的 device_id ≠ notion_browser_id（两个 UUID）——用错 deviceId 触发账号级 429。
- **429 是时间窗口冷却（小时级）**：单次/慢速不受限（用户手动成功、7-23 capture 成功、22:4x 单次 200）；
  连续多发（5 连发、变体连测、多账号连测）触发。冷却恢复时长 ≥90s（实测 90s 间隔仍 429）。
- **不受影响**：validateusercancreateworkspace（200）、runInferenceTranscript（200）、saveTransactionsMain、syncRecordValuesMain。
- **验证路径**：deviceId=notion_browser_id + 完整 16 字段 body → **400 ValidationError**（通过风控层，仅字段值待校正）；
  旧 7 字段 body / 错误 deviceId → 429。

### 当前版本 createspace 完整 body（16 字段，前端 JS 挖出）
```json
{
  "name": "<name>",
  "domain": "<email域>",
  "emailDomains": ["<email域>"],
  "icon": null,
  "planType": "personal",
  "planSelection": "personal",
  "initialPersona": "unfilled",
  "shouldCreateUserPersonaTeam": false,
  "shouldMakeUserPersonaTeamDefault": false,
  "domainType": "personal",
  "collaborativeIntent": null,
  "deviceId": "<notion_browser_id cookie值>",
  "deviceType": "web-desktop",
  "desktopTargetPlatform": "web",
  "source": "handle_root_redirect",
  "createSpaceView": true
}
```
- **createSpaceView:true** → 服务端直接建 space_view，响应带 `spaceViewPointer`（无需手动 saveTransactionsMain 绑定）。
- 响应字段：`spaceId` / `teamId` / `inviteLinkCode` / `spaceViewPointer`。

### 设计对策（贴近真实操作原则）
1. **deviceId 一律从 probe cookies 读 notion_browser_id**（轮换引擎已改）。
2. 轮换引擎内置**保守节流**：单账号创建 ≥10 分钟/次（429 窗口是小时级，10 分钟是下限）。
3. 429 响应 → 长退避（15min→30min），登记 cooldown 到账号状态。
4. 多号池分摊频率；被邀商务空间额度大，轮换需求极低。
5. 研究性验证严禁连发（单次 + ≥20 分钟间隔）。