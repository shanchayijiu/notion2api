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
            name = resp.url.split("/api/v3/")[-1].split("?")[0]
            FULL[name] = resp.text()
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

    ed = page.locator('[contenteditable="true"]').first
    ed.click(timeout=5000)
    ed.fill("Two trains leave the same station at 3pm (60mph) and 4pm (90mph). When do they meet? Think step by step through the full calculation.")
    time.sleep(1)
    page.keyboard.press("Enter")
    print("sent")

    thought_seen = False
    for i in range(30):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
        if "Thought" in v:
            thought_seen = True
            print("THOUGHT BLOCK at t", (i+1)*3)
            break
    print("thought_seen:", thought_seen)
    time.sleep(3)
    v = page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
    print("dialog tail:", v.replace(chr(10), " | ")[-500:])

    # 明文关键词搜索（在全部响应里）
    targets = ["Assuming", "head start", "meet at", "6:00"]
    print("\n=== plaintext search ===")
    for name, body in FULL.items():
        for t in targets:
            if t in body:
                i = body.find(t)
                print(f"FOUND [{t}] in {name} @ {i}")
                print("   ctx:", body[max(0, i-250):i+350][:600])
                out = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\plain_" + name + ".txt")
                out.write_text(body, encoding="utf-8")
                print("   saved full body to", out.name)
    print("responses:", list(FULL.keys())[:15])
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()