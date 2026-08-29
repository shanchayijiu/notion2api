# 循环工作流 #7 情报包（喂给 opus 规划/审查，禁止凭空猜测）

## 用户实测反馈（权威失败信号，优先级最高）
用户用真实 CC 交互测试，三大硬伤：
1. **回复极慢，甚至无法回复** —— 用户感知：发消息后长时间无响应/超时
2. **无流式** —— 用户感知：不是逐字/逐段输出，而是长时间空白后一次性出现（或一直不出）
3. **工具调用一坨** —— 工具不执行/卡死/行为随机

## 框架（请求链路图）
```
CC (15721 cc-switch, apiFormat=anthropic_messages)
  → POST /v1/messages (stream=true)
  → handleMessages: 解析 Anthropic 请求 → 转内部 OpenAI 形态（tools 12 个：Agent/Write/Bash/Read/Edit/Glob/Grep/...）
  → handleMessagesStream:
      goroutine: handleChatCompletions(liveSSEWriter)  ← 2026-08-26 刚改（此前 responseRecorder 全缓冲=伪流式）
        → 内部链路：normalize → 认知重构注入（buildToolBridgePrompt 拼 user 段）→ runInferenceTranscript（Notion 上游）
        → 内部流式 writeChatCompletionLiveStream: 逐块 safeWriteData(writeSSEData → w.Write + Flush)
        → 工具合成（extractToolCalls → synthesizeToolCall → synthesizeTaskCall）
      main: converter.run(pr)  ← 逐行读 OpenAI SSE → 发 Anthropic 事件（message_start/content_block_delta/message_delta/message_stop）
```
关键文件：anthropic.go（入口/管线）、anthropic_stream.go（事件转换器）、main.go（内部 chat 链路）、tool_bridge.go（注入+合成）、workspace_rotation.go（配额轮换）、research/loop_intel_src.md（完整代码片段）

## 已验证事实（前 6 轮实锤）
- CC 走 /v1/messages 会带 12 个工具（openai_chat 时 tools=0）；CC 的 system 提示放在**顶层 system 字段** + **第一条 user 消息的 system-reminder 块**
- CC 的 system 含 "Primary working directory: C:\Users\Administrator\Desktop\cc-test-project"（已实现提取注入）
- CC 版 Agent 工具 required=["description","prompt"]（缺 description 报 InputValidationError，已修）
- 认知重构注入让模型输出工具 JSON 块（当作文档示例）；模型**有时**输出调用块（走 extractToolCalls）、**有时**输出方案文本（走 synthesizeTaskCall 合成 Agent 调用）
- ST 误判已修（CC 的 Claude Code 规则含 "fictional chat between" → 曾误判 SillyTavern 绕过工具桥）
- 账号配额：每号 ~5 推理/小时（quota-exhausted 自动轮换建新空间）；每日 createspace 上限 ~6（429 不可绕）；当前 active=curly.crab.inmn@hidesit.net（ready），colourful.haddock 备用（ready，无冷却）
- Notion 上游 runInferenceTranscript：单次推理 5-15s（流式逐块），长文本（CC 完整 system ~96KB 请求）更慢

## 本轮重点问题（用户三大痛点 → 技术疑点）
1. 无流式：liveSSEWriter 刚改（pipe 实时转发），但**未验证**——内部 safeWriteData 是否逐块实时到达转换器？converter 的 bufio.Scanner 逐行、send+flush 是否实时？**必须实测首 token 延迟**
2. 回复慢/无法回复：
   a. 认知重构注入被塞进 user 消息（请求体 ~96KB，每次推理 token 巨大）→ 首 token 延迟高
   b. 账号配额：5 推理/小时后轮换 → 轮换 180s 超时 → 用户"无法回复"
   c. 上游 Notion 对超大 system 的处理（CC 规则 90KB+）
3. 工具调用烂：
   a. 模型输出随机（调用块 vs 方案文本），合成兜底不稳定
   b. CC 收到 Agent tool_use → 创建子代理（异步）→ 子代理消耗更多配额 → 中途断
   c. 子代理 Bash 步骤不完成（写完文件就停，report.md 需手动跑）

## 本轮任务
基于以上情报（代码在 research/loop_intel_src.md）给出：
A. 三大痛点的根因排序（最可能→最不可能，每个给证据/机理）
B. 修复方案（最小改动、逐项、可验证）
C. 验证方法（每个修复的可观测指标：首 token 延迟 ms、事件流时间戳、配额消耗）
D. 若需改注入/合成策略（让模型稳定输出工具调用而非方案文本），给出具体改法
中文 800 字内。