import json, sys, urllib.request, urllib.error, time

sys.stdout.reconfigure(encoding="utf-8")

BASE = "http://127.0.0.1:8787/v1/chat/completions"
HDRS = {"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"}


def call(body):
    req = urllib.request.Request(BASE, data=json.dumps(body).encode("utf-8"), headers=HDRS)
    return json.loads(urllib.request.urlopen(req, timeout=240).read().decode("utf-8"))


file_content = "第一行: hello notion2api\n第二行: tool bridge works\n第三行: end of file"

# R1: 工具调用（synthesizer 或模型输出）
body1 = {
    "model": "gpt-5.2",
    "messages": [{"role": "user", "content": "请读取文件 C:\\Users\\Administrator\\Desktop\\testfile.txt 的内容并告诉我前几行写了什么"}],
    "tools": [{"type": "function", "function": {"name": "read_file", "description": "读取本地文件内容，参数：path 文件路径, max_lines 最大行数"}}],
    "tool_choice": "auto",
}
r1 = call(body1)
c1 = r1["choices"][0]
tc = c1["message"].get("tool_calls")
print("R1 finish:", c1["finish_reason"])
print("R1 tool_calls:", json.dumps(tc, ensure_ascii=False) if tc else "none")
print("R1 content:", repr(c1["message"].get("content") or "")[:150])

if not tc:
    sys.exit("R1 failed: no tool_calls")

# R2: 客户端执行 + 回填结果
body2 = {
    "model": "gpt-5.2",
    "messages": body1["messages"]
    + [
        {"role": "assistant", "content": "", "tool_calls": tc},
        {"role": "tool", "tool_call_id": tc[0]["id"], "content": file_content},
    ],
    "tools": body1["tools"],
    "tool_choice": "auto",
}
r2 = call(body2)
c2 = r2["choices"][0]
print("\nR2 finish:", c2["finish_reason"])
print("R2 content:", repr(c2["message"].get("content") or "")[:400])
tc2 = c2["message"].get("tool_calls")
print("R2 tool_calls:", json.dumps(tc2, ensure_ascii=False)[:150] if tc2 else "none")