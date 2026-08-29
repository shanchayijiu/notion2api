import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是 review 审查员。上一轮你给了"下一步"清单，我执行了循环 1，请你审查本轮成果并给出下一轮指令。

【循环 1 执行内容】
1. **流式 tool_calls 增量分片**（REQ-TOOL-04）：chat/completions 流式成功路径——净化前提取/合成 → filterCallsToAvailable → normalizeToolArgumentsWithSchema → unmask → 输出分片（首片 index+id+type+name+arguments=""，中间片 128 字节 arguments 增量，末片 delta={} + finish_reason=tool_calls + DONE）。实测：finish=tool_calls、首片 id/name/空 arguments 正确、参数拼接完整（{"max_lines":400,"path":"C:/Users/Administrator/Desktop/testfile.txt"}）、DONE 收尾。
2. **流式 synthesizer**：流式场景原始 messages 已消费 → 用 request.LatestUserPrompt 提取路径合成（实测触发成功）。
3. **tool_choice required 强制**：chat/completions 已有（toolChoiceForced+forcedName → synthesizeToolCall 强制）；responses 补全（decodeResponsesRequestBodyFromRaw 现在返回 payload，取 tool_choice → responsesToolChoiceForced/ForcedName）。实测：无路径场景 required → 合成 search_files + query=README。
4. 全测试绿（go test ok）。

【请你审查】
1. 流式实现有没有协议偏差？（首片/增量/末片/usage chunk 顺序、DONE 后无数据、id 稳定性）
2. 流式 synthesizer 的 LatestUserPrompt 方案有没有坑？（比如多轮流式、tool 回填轮）
3. required 合成的语义：无路径时合成 search_files+query 合理吗？还是该按工具名语义更精准？（search→query、read→需要路径时怎么办？）
4. 下一轮循环做什么？（对照你上次的清单：流式筛子 toolStreamSieve 半截标记、拒绝检测层、parallel_tool_calls:false、孤儿 tool 消息 400、协议字段完整性）

输出：审查清单（编号）+ 下一轮指令（明确到可执行），中文 500 字内。""",
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