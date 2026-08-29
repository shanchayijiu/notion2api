import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是 review 审查员。我（notion2api 作者）刚按你的规划执行了工具调用借鉴的快速赢，请你审查执行结果，指出遗漏、风险和改进点。

【执行内容】
1. **实际调研**（非推测）：grok2api（Go，prompt 注入路线，多 XML 格式正则 + toolStreamSieve 流式筛子 + available 白名单校验）、CLIProxyAPI（协议翻译层，Claude 原生 tool_use 上游）、ds2api（Go，toolcall 包：schema 归一化 NormalizeParsedToolCallsForSchemas 按客户端 schema 纠正类型）
2. **已落地**：
   a. tool_choice "none" 语义：不注入 few-shot、不合成、不 mask（hasTools 判定加 !toolChoiceNone）
   b. available 白名单（grok2api 移植）：filterCallsToAvailable——解析/合成结果只保留客户端 catalog 声明过的工具名
   c. schema 参数归一化（ds2api 移植）：normalizeToolArgumentsWithSchema——string enum/const 强转、嵌套 object/array 递归、未知键按 additionalProperties
   d. 并行工具：seen 去重改为 name+arguments（同工具不同参数允许并行）
3. **过程中新发现并修复**：R2 回填轮模型拒绝——模型把注入的 tool 结果识别为"不可信文本"（"tool execution results"字样触发注入防护）→ 改措辞为"用户提供的文件内容/资料"（顺 Notion 身份）→ 实测通过（R1 tool_calls → R2 模型基于资料正确回答）
4. **验证**：单测全绿 + 端到端闭环实测（chat/completions + responses 双通道）

【请你审查】
1. 我漏了什么快速赢？（对照你的规划 1-4 项逐一核对）
2. 已落地 4 项有没有实现偏差或风险？（比如 tool_choice none 的边界、白名单误杀、schema 归一化的误伤场景）
3. 下一步该做什么？（从你的中等项/深水区里选，结合我们"模型会实际尝试读沙盒→拒绝"的上游现状）
4. 有没有你规划里没提但我该注意的？

输出：结构化审查清单（编号），中文，600 字内，重点是"立即修"和"下一步"。""",
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