import json, sys, urllib.request, urllib.error, time

sys.stdout.reconfigure(encoding="utf-8")

cases = [
    ("tilde", "请读取文件 ~/Desktop/testfile.txt 的内容并告诉我前几行写了什么"),
    ("relative", "请读取文件 ./testfile.txt 的内容并告诉我前几行写了什么"),
    ("workspace", "请读取文件 /workspace/testfile.txt 的内容并告诉我前几行写了什么"),
]

for name, content in cases:
    body = {
        "model": "gpt-5.2",
        "messages": [{"role": "user", "content": content}],
        "tools": [{"type": "function", "function": {"name": "read_file", "description": "读取本地文件内容，参数：path 文件路径, max_lines 最大行数"}}],
        "tool_choice": "auto",
    }
    req = urllib.request.Request(
        "http://127.0.0.1:8787/v1/chat/completions",
        data=json.dumps(body).encode("utf-8"),
        headers={"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"},
    )
    try:
        r = json.loads(urllib.request.urlopen(req, timeout=240).read().decode("utf-8"))
        c = r["choices"][0]
        tc = c["message"].get("tool_calls")
        if tc:
            print(f"[{name}] tool_calls -> {json.dumps(tc[0]['function'], ensure_ascii=False)[:200]}")
        else:
            print(f"[{name}] stop, content={c['message'].get('content','')[:70]!r}")
    except urllib.error.HTTPError as e:
        print(f"[{name}] HTTP {e.code}: {e.read().decode('utf-8')[:100]}")
    time.sleep(3)