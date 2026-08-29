import json, sys, urllib.request, urllib.error, time

sys.stdout.reconfigure(encoding="utf-8")

for model in ["sonnet-4.6", "opus-4.6", "deepseek-v4-flash"]:
    body = {
        "model": model,
        "messages": [{"role": "user", "content": "请读取文件 ~/Desktop/testfile.txt 的内容并告诉我前几行写了什么"}],
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
        name = tc[0]["function"]["name"] if tc else "none"
        print(f"[{model}] finish={c['finish_reason']} | tool: {name}")
    except urllib.error.HTTPError as e:
        print(f"[{model}] HTTP {e.code}")
    time.sleep(2)