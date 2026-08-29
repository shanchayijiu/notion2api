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
RESPS = {}
def on_req(rq):
    if "/api/v3/" in rq.url and rq.method == "POST":
        REQS[rq.url.split("/api/v3/")[-1].split("?")[0]] = rq.post_data
def on_resp(resp):
    try:
        if "/api/v3/" in resp.url:
            RESPS[resp.url.split("/api/v3/")[-1].split("?")[0]] = resp.text()
    except Exception:
        pass
page.on("request", on_req)
page.on("response", on_resp)

try:
    page.goto("https://app.notion.com/ai", wait_until="domcontentloaded", timeout=60000)
    for _ in range(15):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,200):''")
        if "How can I help you today" in v or "Personalize" in v:
            break
    print("url:", page.url)
    time.sleep(5)

    # getAvailableModels 请求
    for name in ["getAvailableModels", "getAIUsageEligibility", "getInferenceModels"]:
        if name in REQS:
            print(f"=== REQ {name}: {REQS[name][:500]}")
        if name in RESPS:
            print(f"=== RESP {name}: {RESPS[name][:800]}")
    # 所有含 model 的请求
    for name in REQS:
        if "model" in name.lower() or "inference" in name.lower():
            print("REQ:", name, ":", (REQS[name] or "")[:300])
    for name in RESPS:
        if "model" in name.lower() or "inference" in name.lower():
            print("RESP:", name, ":", (RESPS[name] or "")[:500])
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()