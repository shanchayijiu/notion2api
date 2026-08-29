import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """请担任 API 兼容层设计顾问。项目是 OpenAI 兼容网关（Go），已有 /v1/chat/completions 和 /v1/responses，现需新增 Anthropic Messages API 兼容端点（/v1/messages），让 Claude Code 等 Anthropic 客户端零配置接入（客户端原生工具传递走此路径）。

【现状】
- 内部链路：请求 → PromptRunRequest → 上游 Notion AI（认知重构注入 + 工具调用合成 + 流式 SSE 输出）
- 已有 OpenAI 格式工具解析（name/arguments）、流式 tool_calls 分片、usage、终止语义
- 客户端实测：Claude Code 走 OpenAI 格式时工具不传递（tools=0），疑 openai_chat 适配层问题，需原生 Anthropic 格式路径

【需要你设计】
A. 请求映射：Anthropic Messages 请求（model/messages[{role,content(string|数组)}]/system(字符串|数组)/tools[{name,description,input_schema}]/tool_choice/max_tokens/temperature/stop_sequences/stream/metadata）→ 内部 PromptRunRequest 的映射要点，特别是：
   - system 数组（含巨长 Claude Code 规则）怎么处理
   - content 数组（text/image/tool_use/tool_result）怎么归一
   - tools 转换：input_schema → OpenAI parameters（json schema 差异：required/properties 兼容）
   - tool_choice（auto/any/tool/stop）
B. 流式事件格式：Anthropic SSE 事件（message_start/content_block_start/content_block_delta{text_delta|input_json_delta}/content_block_stop/message_delta{stop_reason,usage}/message_stop + ping + error）——工具调用（tool_use）的增量分片事件序列怎么发（与 OpenAI tool_calls 分片对应）
C. 响应映射：非流式 {id,type:message,role,content:[{type:tool_use,id,name,input}|{type:text,text}],stop_reason,end_turn/max_tokens/tool_use/stop_sequence,usage{input_tokens,output_tokens}}
D. 错误格式：Anthropic error 结构（{type:error,error:{type,message}}）+ 状态码映射
E. 与现有链路复用：工具合成（synthesizeTaskCall/synthesizeToolCall）输出怎么转 Anthropic tool_use；认知重构注入是否照常
F. 实施顺序（改动小→见效快）+ review 检查点 5 条

输出 A-F，中文 700 字内。""",
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