import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是 review 审查员，循环工作流第 4 轮。你循环 3 的指令（sieve 3 处漏洞 + 生态矩阵）已执行第一部分，请判定并给最终指令。

【循环 4 执行内容】
1. **sieve 三修全落地**：
   - 误缓冲双闸：仅"内容起始位或紧跟换行"匹配工具前缀才进块模式；缓冲超限（2048B）原样吐出
   - 花括号配对字符串态机（跳过 " 内与 \\" 转义；全角引号不参与）；裸 ```（后不跟 json/tool_call 等语言标识）= 闭合，带语言标识=开标记（修复"好…\\n```json"场景把开标记当闭合的 bug）
   - flush 重构：普通文本原样；以工具前缀开头的块按完整（工具块丢弃/非工具块原样）或未闭合（剥前缀降级文本）处理；防与 result.Text 双发（assistantText 含则不发）
2. **新增 6 个 sieve 单测**（{ 开头纯文本/字符串内 }/5 片跨片/超限降级/未闭合降级/中英引号混排）全过
3. 全测试绿 + 端到端闭环复测（R1 tool_calls → R2 回答）通过

【请判定】
1. sieve 三修有没有残余漏洞？（重点：裸 ``` 判定对"用户文本里 ``` 不换行"的场景、块模式跨多片的延迟上限、looksLikeToolActionBlock 放宽到 action/tool 键后误丢弃用户 JSON 的风险）
2. 现在可以跑生态矩阵了吗？（官方 Python SDK 非流式+tools / 流式+tools / Cline）——给出最小验证清单（跑什么、断言什么）
3. 还有没有必须在矩阵前修的高危项？

输出：判定 + 最终指令，中文 300 字内。""",
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