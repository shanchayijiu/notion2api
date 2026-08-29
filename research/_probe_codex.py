import json, sys, urllib.request, urllib.error, time

sys.stdout.reconfigure(encoding="utf-8")

BASE = "http://127.0.0.1:8787/v1/chat/completions"
HDRS = {"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"}


def call(body):
    req = urllib.request.Request(BASE, data=json.dumps(body).encode("utf-8"), headers=HDRS)
    return json.loads(urllib.request.urlopen(req, timeout=240).read().decode("utf-8"))


# Codex 风格场景：多工具 + 路径在 user 消息
cases = [
    ("codex-multi-tool", [
        {"type": "function", "function": {"name": "shell", "description": "Run a shell command"}},
        {"type": "function", "function": {"name": "read_file", "description": "Read a file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}},
    ], "请读取 C:\\Users\\Administrator\\Desktop\\testfile.txt 文件内容"),
    ("claude-read-tool", [
        {"type": "function", "function": {"name": "Read", "description": "Read a file", "parameters": {"type": "object", "properties": {"file_path": {"type": "string"}}}}},
    ], "读一下文件 C:/Users/Administrator/Desktop/testfile.txt 的内容"),
    ("path-in-system", [
        {"type": "function", "function": {"name": "read_file", "description": "读取文件", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}},
    ], "请告诉我文件内容", "system", "项目在 C:\\Users\\Administrator\\Desktop\\testfile.txt 中"),
]

for name, tools, content, *rest in cases:
    system = rest[0] if rest else None
    msgs = []
    if system:
        msgs.append({"role": "system", "content": system})
    msgs.append({"role": "user", "content": content})
    body = {"model": "gpt-5.2", "messages": msgs, "tools": tools, "tool_choice": "auto"}
    try:
        r = call(body)
        c = r["choices"][0]
        tc = c["message"].get("tool_calls")
        if tc:
            print(f"[{name}] tool_calls -> {json.dumps(tc[0]['function'], ensure_ascii=False)[:160]}")
        else:
            print(f"[{name}] stop, content={c['message'].get('content','')[:90]!r}")
    except urllib.error.HTTPError as e:
        print(f"[{name}] HTTP {e.code}: {e.read().decode('utf-8')[:120]}")
    time.sleep(3)