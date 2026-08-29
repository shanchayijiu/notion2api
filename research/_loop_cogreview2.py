import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """请担任提示词工程审查员（第二轮）。继续审查 few-shot 模板的格式一致性问题。

【上一轮你的建议已全部落地 + 实测结果】
落地：
1. 硬约束声明："each example MUST be exactly one JSON object with top-level keys name(string) and arguments(object). Parameters are ALWAYS nested inside arguments; never flatten parameters to the top level. Schema: {\"required\":[\"name\",\"arguments\"],\"additionalProperties\":false}. Output ONLY the JSON object with no surrounding explanation text."
2. 工具描述带参数表（- **read_file**: 读取本地文件 (path: string, max_lines?: integer)）
3. 多示例同构（2 个示例，键名一致）+ 负例对照（WRONG — do not use: {"operation": ...}）
4. 解析器兼容面：name/function/tool/tool_name/action/operation 别名归一 + arguments 字符串二次 parse + 扁平参数收拢
5. 正文污染抑制："Output ONLY the JSON object with no surrounding explanation text"

【实测（真实上游，改动前后对比）】
- 改动前：模型输出扁平变体 {"operation":"read_file","path":"~/..."}（键名漂移）+ 附带解释文本"先用这个示例块："
- 改动后：模型严格输出 {"name":"read_file","arguments":{"path":"~/Desktop/testfile.txt"}}，无解释文本 → 服务端解析为标准 tool_calls（name=read_file, arguments 含 unmask 后的绝对路径）

【审查清单】
A. 模板还有没有可改进点（保持"改动小→见效快"原则）？比如：多参数示例要不要加第三个（带非字符串参数）？路径格式要不要再显式约束？
B. 正文污染抑制是否够（会不会偶发仍带解释文本？服务端对"示例块前后文本"的容忍策略是否合理——剥离标记只取块？）
C. 兼容面的副作用：解析器放宽键名变体会不会误伤正常正文里的 JSON（比如文档里恰好有 {"name":"x"} 对象）？误报率和防线怎么设？
D. 这个方案（文档框架 + 严格契约 + 负例）在格式稳定性上还有没有已知脆弱点？上游提示词更新后失效的应对（分级降级：严格契约 → 扁平兼容 → synthesizer）？
E. 检查点 5 条。

输出 A-E，中文 500 字内。""",
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