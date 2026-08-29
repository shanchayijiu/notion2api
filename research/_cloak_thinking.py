import json, pathlib, sys, time, uuid
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

def vis(page):
    try:
        return page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
    except Exception:
        return ""

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

NDJSON = []
def on_response(resp):
    if "runInferenceTranscript" in resp.url:
        try:
            NDJSON.append((resp.status, resp.text()))
        except Exception:
            pass
page.on("response", on_response)

try:
    # /ai 对话界面
    page.goto("https://app.notion.com/ai", wait_until="domcontentloaded", timeout=60000)
    for _ in range(20):
        time.sleep(3)
        v = vis(page)
        if "How can I help you today" in v or "Personalize" in v:
            break
    print("url:", page.url)
    print("body head:", vis(page).replace(chr(10), " | ")[:250])

    # 输入框（contenteditable）
    try:
        ed = page.locator('[contenteditable="true"]').first
        ed.click(timeout=5000)
        ed.fill("Think step by step: how many letters are in the word elephant? Show your full reasoning, then give the final answer.")
        print("filled prompt")
        time.sleep(2)
        page.keyboard.press("Enter")
        print("sent")
    except Exception as e:
        print("send ERR:", str(e)[:150])

    # 等 AI 回复（流式），观察 thinking 显示
    for i in range(40):
        time.sleep(3)
        v = vis(page)
        vl = v.lower()
        # 捕捉关键信号
        if "thinking" in vl or "reasoning" in vl or "thought" in vl:
            print(f"  t{(i+1)*3}s >> THINKING MARKER:", v[-800:].replace(chr(10), " | ")[:400])
        if i % 6 == 0:
            print(f"  t{(i+1)*3}s len={len(v)}", v.replace(chr(10), " | ")[-250:])
        if "elephant" in v and "8" in v and i > 6:
            print("  done marker")
            break
    v = vis(page)
    print("FINAL:", v.replace(chr(10), " | ")[-1800:])
    print("NDJSON responses:", len(NDJSON))
    for st, body in NDJSON[:1]:
        print("  status:", st, "len:", len(body))
        enc = body.count("encryptedContent")
        print("  encryptedContent x", enc)
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()