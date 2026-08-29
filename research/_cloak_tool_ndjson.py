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

NDJSON = {}
def on_resp(resp):
    try:
        if "runInferenceTranscript" in resp.url:
            NDJSON["body"] = resp.text()
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
    ed.fill("Use the filesystem tools to read modules/fs/index.ts and tell me its first line.")
    time.sleep(1)
    page.keyboard.press("Enter")
    print("sent")
    for i in range(30):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,2000):''")
        if "Module" in v and i > 4:
            break
    time.sleep(4)

    body = NDJSON.get("body", "")
    out = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\raw_tool2.jsonl")
    out.write_text(body, encoding="utf-8")
    print("saved ndjson len:", len(body))
    # 分析 tool 事件
    for i, ln in enumerate(body.splitlines()):
        if not ln.strip():
            continue
        if "tool" in ln.lower():
            try:
                j = json.loads(ln)
            except Exception:
                continue
            for op in j.get("v", []):
                nv = op.get("v")
                if isinstance(nv, dict):
                    t = str(nv.get("type", ""))
                    if "tool" in t.lower():
                        print(f"--- line {i} p={op.get('p','')} type={t}")
                        print("   ", json.dumps(nv, ensure_ascii=False)[:600])
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()