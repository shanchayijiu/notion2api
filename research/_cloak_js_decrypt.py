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

try:
    page.goto("https://app.notion.com/ai", wait_until="domcontentloaded", timeout=60000)
    for _ in range(15):
        time.sleep(3)
        if len(page.evaluate("()=>document.body?document.body.innerText.slice(0,200):''")) > 60:
            break
    print("url:", page.url)

    # 页面上下文：fetch 所有已加载 JS，搜解密相关代码
    result = page.evaluate("""async () => {
        const entries = performance.getEntriesByType('resource')
            .map(e => e.name).filter(u => u.includes('_assets') && u.endsWith('.js'));
        const out = {encryptedRefs: [], decryptFns: [], keyRefs: []};
        for (const u of entries) {
            try {
                const t = await (await fetch(u)).text();
                if (t.includes('encryptedContent')) {
                    const i = t.indexOf('encryptedContent');
                    out.encryptedRefs.push({url: u.split('/').pop(), ctx: t.slice(Math.max(0,i-200), i+200)});
                }
                if (/decrypt|decipher|subtle\.decrypt|aesCbc|aesGcm/i.test(t)) {
                    for (const m of t.matchAll(/(.{80}(?:decrypt|decipher|subtle\\.decrypt|aesCbc|aesGcm).{80})/gi)) {
                        out.decryptFns.push(m[1].replace(/\\s+/g, ' '));
                    }
                }
            } catch (e) {}
        }
        return out;
    }""")
    print("encryptedContent refs:", len(result.get("encryptedRefs", [])))
    for r in result.get("encryptedRefs", [])[:3]:
        print(" == ", r.get("url"))
        print("    ", (r.get("ctx") or "")[:300])
    print("decrypt fns:", len(result.get("decryptFns", [])))
    for d in result.get("decryptFns", [])[:5]:
        print(" == ", d[:200])
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()