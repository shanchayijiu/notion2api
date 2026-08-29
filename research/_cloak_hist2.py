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

RESPS = {}
def on_resp(resp):
    try:
        if "/api/v3/" in resp.url or "msgstore" in resp.url:
            RESPS[resp.url.split("/api/v3/")[-1].split("?")[0] or resp.url.split("/")[-1]] = resp.text()
    except Exception:
        pass
page.on("response", on_resp)

try:
    page.goto("https://app.notion.com/chat", wait_until="domcontentloaded", timeout=60000)
    for _ in range(20):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,1500):''")
        if "Train meeting time problem" in v or "Recents" in v:
            break
    print("url:", page.url)
    v = page.evaluate("()=>document.body?document.body.innerText.slice(0,1500):''")
    print("chat page:", v.replace(chr(10), " | ")[:300])

    # 点击 Recents 里的 train 对话
    try:
        loc = page.locator('text=Train meeting time problem').first
        if loc.count():
            loc.click(timeout=6000)
            print("clicked train conversation")
            time.sleep(8)
    except Exception as e:
        print("click ERR:", str(e)[:120])

    v = page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
    print("HAS THOUGHT:", "Thought" in v)
    print("dialog:", v.replace(chr(10), " | ")[-700:])

    # 明文搜索
    target = "Assuming both trains"
    print("\n=== plaintext sources ===")
    for name, body in RESPS.items():
        if target in body:
            i = body.find(target)
            print(f"FOUND in {name} @ {i}")
            print("  ctx:", body[max(0, i-200):i+400][:600])
    print("responses captured:", len(RESPS))
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()