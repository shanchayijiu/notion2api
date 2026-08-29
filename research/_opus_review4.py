import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是 review 审查员，循环工作流第 3 轮。你上一轮指令（usage 门控 + stop 回归 + toolStreamSieve）已执行，请判定目标进度并给下一轮指令。

【循环 3 执行内容】
1. **usage 门控确认**：usage 片仅在 stream_options.include_usage=true 时发（既有 includeUsage 条件），choices 空数组
2. **stop 路径回归测试** TestStreamPlainTextStopPath：role → content → finish=stop（delta 空）→ DONE；finish_reason:null 补丁不破坏终片——已过（顺带修了测试消息命中身份探针的问题）
3. **toolStreamSieve 半截标记缓冲**（grok2api 移植）：
   - feed(delta)：缓冲尾部；buffer 以工具标记前缀开头（```json/<tool_call/{"action"/{"tool"/{"name" 等 8 类）→ 进入块模式缓冲不外泄
   - 块模式：找闭合（```/</tool_call>/JSON 花括号配对）→ 判定 looksLikeToolActionBlock → 工具块整块丢弃（由 result 后 tool_calls 分片输出）/非工具块原样吐出
   - flush()（流终止）：未闭合块降级为文本（剥标记前缀，REQ-SAN-15 策略 A）
   - 接线：emitContent 经 sieve；错误路径 partialText 并入 sieve.flush；成功路径无 toolCalls 时发 sieveTail
4. 全测试绿

【请判定】
1. sieve 实现有没有漏洞？（比如：纯文本以"{"开头被误缓冲、块模式跨多片、flush 与 result.Text 重复、中文/引号边界）
2. 你上次说"完成 1+2 后即可跑生态矩阵"——现在该跑生态矩阵了吗？怎么跑最省（官方 Python SDK 非流式+流式工具各一轮 / Cline 配置）？
3. 或还有必须先修的高危项？

输出：判定 + 下一轮指令（可执行），中文 350 字内。""",
        }
    ],
}

req = urllib.request.Request(
    "http://127.0.0.1:8787/v1/chat/completions",
    data=json.dumps(body).encode("utf-8"),
    headers={"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"},
)
try:
    r = json.loads(urllib.request.urlopen(req, timeout=300).read().decode("utf-8"))
    print(r["choices"][0]["message"]["content"])
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:300])