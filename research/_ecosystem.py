import json, sys

sys.stdout.reconfigure(encoding="utf-8")

from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8787/v1", api_key="sk-notion2api-dev")

TOOLS = [{
    "type": "function",
    "function": {
        "name": "read_file",
        "description": "读取本地文件内容，参数：path 文件路径, max_lines 最大行数",
        "parameters": {
            "type": "object",
            "properties": {
                "path": {"type": "string", "description": "文件路径"},
                "max_lines": {"type": "integer", "description": "最大行数"},
            },
            "required": ["path"],
        },
    },
}]

MSG = {"role": "user", "content": "请读取文件 C:\\Users\\Administrator\\Desktop\\testfile.txt 的内容并告诉我前几行写了什么"}

# 1) 非流式 + tools
print("=== 1. 非流式 + tools ===")
resp = client.chat.completions.create(model="gpt-5.2", messages=[MSG], tools=TOOLS, tool_choice="auto")
msg = resp.choices[0].message
print("finish_reason:", resp.choices[0].finish_reason)
print("content is None/empty:", msg.content is None or msg.content == "")
assert resp.choices[0].finish_reason == "tool_calls", "finish_reason must be tool_calls"
assert msg.tool_calls and len(msg.tool_calls) >= 1, "tool_calls missing"
tc = msg.tool_calls[0]
print("tool:", tc.function.name)
args = json.loads(tc.function.arguments)
print("args:", args)
assert tc.function.name == "read_file" and "path" in args, "tool call wrong"
assert "```" not in (msg.content or ""), "marker leaked"
print("PASS")

# 2) 流式 + tools
print("\n=== 2. 流式 + tools ===")
stream = client.chat.completions.create(model="gpt-5.2", messages=[MSG], tools=TOOLS, tool_choice="auto", stream=True)
tool_calls_acc = {}
finish = None
done = False
first_byte_delay_ok = True
for chunk in stream:
    if chunk.choices is None or len(chunk.choices) == 0:
        continue
    choice = chunk.choices[0]
    if choice.finish_reason:
        finish = choice.finish_reason
    if choice.delta.tool_calls:
        for tc in choice.delta.tool_calls:
            idx = tc.index
            if idx not in tool_calls_acc:
                tool_calls_acc[idx] = {"id": tc.id, "name": tc.function.name if tc.function else None, "args": ""}
            if tc.function and tc.function.arguments:
                tool_calls_acc[idx]["args"] += tc.function.arguments
print("finish:", finish)
assert finish == "tool_calls", f"finish must be tool_calls, got {finish}"
assert len(tool_calls_acc) >= 1, "no tool calls in stream"
for idx, acc in tool_calls_acc.items():
    args = json.loads(acc["args"])
    print(f"tool[{idx}]: {acc['name']} args={args}")
    assert acc["name"] == "read_file" and "path" in args, "stream tool call wrong"
    assert acc["id"], "stream tool call missing id"
print("PASS")

# 3) 多轮回填（SDK 标准循环）
print("\n=== 3. 多轮工具循环（回填） ===")
file_content = "第一行: hello notion2api\n第二行: tool bridge works\n第三行: end of file"
messages = [MSG]
for _ in range(3):
    resp = client.chat.completions.create(model="gpt-5.2", messages=messages, tools=TOOLS, tool_choice="auto")
    msg = resp.choices[0].message
    if resp.choices[0].finish_reason == "tool_calls":
        messages.append(msg)
        for tc in msg.tool_calls:
            messages.append({"role": "tool", "tool_call_id": tc.id, "content": file_content})
        print("round: tool_calls ->", tc.function.name, tc.function.arguments)
        continue
    print("round finish: stop")
    print("answer:", msg.content[:120])
    assert "hello notion2api" in msg.content, "answer must include file content"
    print("PASS")
    break
else:
    sys.exit("FAIL: tool loop never terminates")

print("\n=== ALL PASS ===")