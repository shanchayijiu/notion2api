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

REQS = {}
def on_req(rq):
    if "/api/v3/" in rq.url and rq.method == "POST":
        REQS[rq.url.split("/api/v3/")[-1].split("?")[0]] = rq.post_data
page.on("request", on_req)

RESPS = {}
def on_resp(resp):
    try:
        if "/api/v3/" in resp.url:
            RESPS[resp.url.split("/api/v3/")[-1].split("?")[0]] = resp.text()
    except Exception:
        pass
page.on("response", on_resp)

try:
    # 直接打开 train 历史对话（thread URL 格式：/chat?t=<thread_id> 或 /<thread_id>?）
    THREAD = "3c68c1ed-b9f1-803b-bd5f-00a98b40e68b"
    page.goto(f"https://app.notion.com/chat?t={THREAD}", wait_until="domcontentloaded", timeout=60000)
    for _ in range(20):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,2500):''")
        if "Thought" in v:
            print("Thought block rendered!")
            break
        if "Train meeting time problem" in v:
            break
    print("url:", page.url)
    v = page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
    print("HAS THOUGHT:", "Thought" in v)
    print("body:", v.replace(chr(10), " | ")[-900:])

    # 找明文 thinking 在哪个响应
    target_kw = "Assuming both trains"
    print("\n=== searching plaintext in responses ===")
    for name, body in RESPS.items():
        if target_kw in body:
            i = body.find(target_kw)
            print(f"FOUND in {name} @ {i}")
            print("  ctx:", body[max(0, i-300):i+500][:800])
            (pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\plain_src_" + name + ".json")
             .write_text(body, encoding="utf-8"))
            print("  saved full body")
    print("=== relevant requests ===")
    for name, body in REQS.items():
        if "transcript" in name or "thread" in name or "message" in name or "sync" in name:
            print("REQ", name, ":", (body or "")[:200])
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()