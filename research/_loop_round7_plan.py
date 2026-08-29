import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")
src = open(r'C:\Users\Administrator\notion2api\research\loop_intel_src_compact.md', encoding='utf-8').read()
accounts = open(r'C:\Users\Administrator\notion2api\research\loop_intel_accounts.txt', encoding='utf-8').read()

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": (
                "你是本项目架构顾问（循环工作流第 7 轮）。真实失败情报包，禁止凭空猜测。\n\n"
                "## 用户实测三大硬伤\n1. 回复极慢甚至无法回复 2. 无流式（长时间空白后一次性出现）3. 工具调用一坨（不执行/卡死/随机）\n\n"
                "## 框架\nCC(15721, anthropic_messages) → /v1/messages(stream) → handleMessages(转内部 OpenAI 形态,tools 12)\n→ handleMessagesStream: goroutine handleChatCompletions(liveSSEWriter) [内部链路: 认知重构注入→runInferenceTranscript(Notion 上游)→writeChatCompletionLiveStream 逐块 safeWriteData(Write+Flush)]; main converter.run(pr) 逐行转 Anthropic 事件\n→ 工具合成: extractToolCalls→synthesizeToolCall→synthesizeTaskCall(方案文本→合成 Agent)\n\n"
                "## 代码（真实，当前状态）\n" + src + "\n\n"
                "## 账号\n" + accounts + "\n（每号 ~5 推理/小时后 quota-exhausted 自动轮换建新空间，每日 createspace 429 不可绕，轮换超时 180s）\n\n"
                "## 已验证事实\n- CC /v1/messages 带 12 工具；system 在顶层 + 第一条 user 消息 system-reminder 块；含 Primary working directory（已提取注入）\n"
                "- Agent 工具 required=[description,prompt]（已补）\n- 模型输出随机：有时 JSON 调用块（extract），有时方案文本（合成 Agent）\n- ST 误判已修\n- Notion 上游单次推理 5-15s；CC 请求体 ~96KB\n\n"
                "## 任务（中文 700 字内）\nA. 三大痛点根因排序（最可能→最不可能，给机理，引用代码函数名）\nB. 修复方案（最小改动，具体到函数/段，可验证）\nC. 每个修复的可观测验证指标（首 token 延迟/事件流时间戳/配额）\nD. 注入/合成策略是否改（让模型稳定输出调用块而非方案文本），怎么改"
            ),
        }
    ],
}

req = urllib.request.Request(
    "http://127.0.0.1:8787/v1/chat/completions",
    data=json.dumps(body).encode("utf-8"),
    headers={"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"},
)
try:
    r = json.loads(urllib.request.urlopen(req, timeout=600).read().decode("utf-8"))
    text = r["choices"][0]["message"]["content"]
    open(r'C:\Users\Administrator\notion2api\research\loop_round7_plan.txt', 'w', encoding='utf-8').write(text)
    print(text)
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:400])