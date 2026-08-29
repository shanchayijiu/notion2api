import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是规划者（循环工作流第 1 步：规划）。我是 notion2api 作者（Go，Notion AI → OpenAI 兼容桥，127.0.0.1:8787）。目标是：**让 Claude Code (CC) 和 Codex 这两个 agent/harness 客户端调用我们的服务，效果跟普通 API 一样**。用户实测：CC/Codex 里"根本不会回消息"。

【已确认的事实（我实测的）】
1. CC 2.1.241：`ANTHROPIC_BASE_URL` 配置**不生效**（请求没到服务）；必须用 `CLAUDE_CODE_USE_OPENAI_ROUTER=1` + `OPENAI_BASE_URL=http://127.0.0.1:8787/v1` + `OPENAI_API_KEY` + `OPENAI_MODEL=gpt-5.2`——此时基础消息能回（"reply with OK only" → OK）
2. **已修**：CC 端点探测 GET / 返回了 HTML → 改成 JSON（Accept 含 json 或带 Authorization 时返回 {"object":"list","data":[...]}）——修复后基础对话通了
3. 工具场景未通：CC 里 "读取文件并告诉我内容"（--allowedTools Read）→ 输出乱码/无有效响应（可能是 max-turns 耗尽或响应异常）
4. 服务没有 `/v1/messages`（Anthropic Messages API）——CC 原生格式 404
5. Codex 本机未装 CLI（用户环境可能有）；Codex 用 /v1/responses——我们已补 responses 非流式工具链路（function_call 输出项），但 **responses 流式事件**（response.output_text.delta / response.function_call_arguments.delta 等）是否完整未验证
6. 我们的流式是"上游收完整轮再分片发"（非真增量），TTFB 依赖上游完成
7. 工具链路现状：chat completions 非流式/流式 tool_calls 全协议 + synthesizer + 白名单 + schema 归一化 + sieve，官方 Python SDK 3 项矩阵已过

【请你规划】
1. CC/Codex agent 接入的完整差距清单（按"会阻断使用"程度排序）——基于你的知识（CC 的 OpenAI router 模式要求什么字段/探测/流式；Codex 的 responses 流式事件要求）
2. 每一项的验证方法（怎么确认修好了）
3. 执行顺序（先做什么能最快让 CC 工具场景跑通）
4. 明确哪些是"必须修"哪些是"可选优化"

输出：结构化差距清单（编号，每项：差距/影响/验证方法/优先级），中文，700 字内。""",
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