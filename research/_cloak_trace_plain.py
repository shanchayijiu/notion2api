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

TRACE = []
def on_resp(resp):
    try:
        url = resp.url
        ct = resp.headers.get("content-type", "")
        if "/api/v3/" in url or "msgstore" in url or "socket" in url or "ws" in url.lower():
            body = ""
            try:
                body = resp.text()[:2000]
            except Exception:
                pass
            TRACE.append({"t": "resp", "url": url[:150], "ct": ct[:50], "body": body})
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
    print("url:", page.url)

    ed = page.locator('[contenteditable="true"]').first
    ed.click(timeout=5000)
    ed.fill("A bat and a ball cost $1.10 total. The bat costs $1.00 more than the ball. How much does the ball cost? Walk through your full thought process carefully.")
    time.sleep(1)
    page.keyboard.press("Enter")
    print("sent")

    # 等回复完成
    for i in range(30):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
        if "Thought" in v and "5" in v and i > 4:
            print("thought block visible at t", (i+1)*3)
            break
    time.sleep(3)

    # 找明文 thinking 出现在哪个网络响应
    target = "bat and a ball cost"  # 思考内容关键词（ball 答案推理）
    print("\n=== responses containing plaintext thought ===")
    found = []
    for tr in TRACE:
        if tr["t"] == "resp":
            if "Assume" in tr["body"] or "cost" in tr["body"].lower() and "$1" in tr["body"]:
                found.append(tr)
    print("matches:", len(found))
    for f in found[:10]:
        print(" URL:", f["url"])
        print(" CT:", f["ct"])
        print(" BODY[:800]:", f["body"][:800])
        print()
    print("total responses captured:", len(TRACE))
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()