import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你在帮我规划一个技术借鉴任务。我是 notion2api 的作者（Go 实现的 Notion AI → OpenAI 兼容桥）。

【我的现状（工具调用部分）】
1. few-shot 注入：user 指令（"工作区文档整理"框架，避免身份对抗）+ assistant 完整输出示例（```json action 块）→ 追加在请求组装末端
2. 多格式解析：extractToolCalls 支持 <tool_call> XML / ```json fence / 裸 JSON / 扁平格式（{"action":"read_file","path":...}）/ c2a 格式（{"tool":...,"parameters":...}）
3. synthesizer：模型拒答（文本含路径+拒绝语义）→ 从请求提取路径 → 服务端合成 tool_calls（仅首轮）
4. 路径伪装：绝对路径 C:\\... → ~/ 工作区路径（Notion 模型拒绝"本地路径"）→ unmask 还原
5. 多轮闭环：tool 结果回填注入 → 模型基于结果回答（实测通过）
6. 原生透传：Notion NDJSON agent-tool-result 事件（fs.readFiles 等）收集后透传 tool_calls
7. 工具调用已验证场景：read_file（读本地文件）单工具闭环

【要借鉴的优质 2api 项目】
- https://github.com/chenyme/grok2api （Grok 逆向）
- https://github.com/router-for-me/CLIProxyAPI （Claude 类 API 代理）
- https://github.com/CJackHwang/ds2api （DeepSeek 逆向）
这三个我实际用过，可用性较好。

【任务】帮我规划借鉴方案：
1. 这三个项目各自在"工具调用/tool_calls"上怎么实现的（如果知道，按你对这类项目的了解推测也行，标注推测）
2. 哪些机制值得我借鉴（优先级排序，理由）
3. 建议的落地步骤（按重要性/成本排序，标注哪些是快速赢、哪些是深水区）
4. 有没有我现状里明显缺失的工具调用能力（比如并行工具、tool_choice 完整语义、流式增量分片、多轮循环深度）

输出格式：结构化规划（编号列表），中文，控制在 800 字以内，重点是可执行的动作清单。""",
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