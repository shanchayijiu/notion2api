import json, pathlib, sys, time
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from cloakbrowser import launch

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
ACC = "mt6rrdq5mb9r@imageeditgpt.com"
info = json.loads((DETAIL / ACC / "probe.json").read_text(encoding="utf-8"))

cookies = []
seen = set()
for c in info.get("cookies", []):
    if not c.get("value"):
        continue
    for dom in [".notion.com", ".notion.so"]:
        k = (dom, c["name"])
        if k in seen:
            continue
        seen.add(k)
        cookies.append({"name": c["name"], "value": c["value"], "domain": dom,
                        "path": "/", "httpOnly": False, "secure": True, "sameSite": "Lax"})

browser = launch(headless=True, proxy="http://127.0.0.1:7890", humanize=True, locale="en-US")
ctx = browser.new_context(
    user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
    viewport={"width": 1440, "height": 900}, locale="en-US",
    extra_http_headers={
        "notion-client-version": info.get("client_version") or "23.13.20260720.1949",
        "x-notion-active-user-header": info["user_id"],
        "x-notion-space-id": info["space_id"],
    })
ctx.add_cookies(cookies)
page = ctx.new_page()

FULL = {}
def on_resp(resp):
    try:
        if "/api/v3/" in resp.url:
            FULL[resp.url.split("/api/v3/")[-1].split("?")[0]] = resp.text()
    except Exception:
        pass
page.on("response", on_resp)

try:
    page.goto("https://app.notion.com/ai", wait_until="domcontentloaded", timeout=60000)
    for _ in range(15):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,300):''")
        if "How can I help you today" in v or "Personalize" in v:
            break

    # 选 Claude 模型（Opus）——打开模型菜单
    try:
        loc = page.locator('text=Auto').first
        if loc.count():
            loc.click(timeout=5000)
            time.sleep(2)
            v = page.evaluate("()=>document.body?document.body.innerText.slice(0,1200):''")
            print("model menu:", v.replace(chr(10), " | ")[-500:])
            for m in ["Opus 5", "Opus 4.8", "Claude", "opus"]:
                try:
                    l2 = page.locator(f'text={m}').first
                    if l2.count():
                        l2.click(timeout=4000)
                        print("selected:", m)
                        break
                except Exception:
                    pass
            time.sleep(2)
    except Exception as e:
        print("model ERR:", str(e)[:100])

    ed = page.locator('[contenteditable="true"]').first
    ed.click(timeout=5000)
    ed.fill("If a train leaves at 3pm going 60mph and another at 4pm going 90mph, when do they meet? Think through your reasoning.")
    time.sleep(1)
    page.keyboard.press("Enter")
    print("sent")

    for i in range(30):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
        if "Thinking" in v or "Thought" in v or "meet at" in v:
            print("state at t", (i+1)*3, ":", [k for k in ["Thinking", "Thought"] if k in v])
        if "6:00" in v and i > 5:
            break
    time.sleep(5)

    # 分析响应：sync 和 ndjson 里 thinking 的明文/加密
    target = "head start"
    for name in ["syncRecordValuesSpaceInitial", "runInferenceTranscript"]:
        body = FULL.get(name, "")
        if not body:
            continue
        enc = body.count("encryptedContent")
        plain = 0
        # 找明文 thinking（content 非空）
        for kw in [target, "60mph", "t ="]:
            if kw in body:
                plain += 1
        print(f"{name}: len={len(body)} encryptedContent={enc} plaintext_hits={plain}")
        if target in body:
            i = body.find(target)
            print("  plain ctx:", body[max(0, i-150):i+250][:400])
        # 统计 thinking type
        import re
        t_count = body.count('"type":"thinking"')
        t2 = body.count('"type": "thinking"')
        print(f"  thinking items: {t_count + t2}")
        out = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\claude_" + name + ".txt")
        out.write_text(body, encoding="utf-8")
        print("  saved", out.name)
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()