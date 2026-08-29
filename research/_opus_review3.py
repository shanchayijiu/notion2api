import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是 review 审查员，这是循环工作流的第 2 轮。上一轮你给了循环 1 的审查（1.1-1.6 流式协议、2.1 回填轮禁合成、3 required 语义），我已执行。请你审查并判定：还有没有阻塞"主流客户端工具调用可用"这一目标的项？

【循环 2 执行内容】
1. **流式协议修正**（1.1-1.6 全落地）：
   - role 首片（delta:{role:assistant, content:null}）先发
   - 每片带同 id/created/model/object + index:0 + finish_reason 显式 null（buildChatStreamDeltaChoice 补 finish_reason:nil）
   - 增量片只 {index, function:{arguments}}，不重复 id/type/name
   - 128 字节按字节切（允许切断 UTF-8/JSON 转义，客户端字符串拼接安全）
   - 片顺序：role → 工具首片 → 增量片 → finish 片（delta={}+finish_reason=tool_calls）→ usage 片（choices 空数组，可选）→ DONE
2. **流式 synthesizer 回填轮禁合成**（2.1）：ToolBridgeSection 含 "The user has provided"（结果注入标记）→ 禁止合成
3. **黄金测试 TestStreamToolCallProtocol**：断言 role 首片/工具首片字段/增量片纯净/中文路径跨片完整/finish 片/usage 顺序/DONE 唯一——已过
4. required 语义（3）：chat completions 已有 forcedName 强制合成；responses 补了 tool_choice 解析（responsesToolChoiceForced/ForcedName）；合成参数按工具类型（search→query、read→path 提取不到则跳过合成——当前实现：path 为空且非强制时 isReadIntent=false 不合成，避免编造）
5. 全测试绿（go test ok）

【请判定】
1. 上述修正有没有实现偏差？（重点：finish_reason 显式 null 会不会破坏现有 stop 路径测试？usage 片 choices 空数组在 include_usage=false 时是否不应发？）
2. "主流客户端工具调用可用"目标还差什么？（对照你上次的清单：toolStreamSieve 半截标记、拒绝检测层、parallel_tool_calls:false、孤儿 tool 消息 400、协议字段完整性、生态矩阵验证）
3. 下一轮循环做哪 1-2 项最优先？（明确到可执行）
4. 或者：如果认为目标已达成/接近，给出验收标准清单让我跑生态验证。

输出：判定 + 下一轮指令，中文 400 字内。""",
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