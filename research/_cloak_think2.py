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

def vis(page):
    try:
        return page.evaluate("()=>document.body?document.body.innerText.slice(0,4000):''")
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

try:
    page.goto("https://app.notion.com/ai", wait_until="domcontentloaded", timeout=60000)
    for _ in range(20):
        time.sleep(3)
        v = vis(page)
        if "How can I help you today" in v or "Personalize" in v:
            break
    print("url:", page.url)

    # 发一条（保持对话）
    try:
        ed = page.locator('[contenteditable="true"]').first
        ed.click(timeout=5000)
        ed.fill("What is 17 times 23? Show your complete step-by-step reasoning.")
        time.sleep(1)
        page.keyboard.press("Enter")
        print("sent")
    except Exception as e:
        print("send ERR:", str(e)[:120])

    # 等回复 + 观察
    for i in range(30):
        time.sleep(3)
        v = vis(page)
        if "391" in v and i > 4:
            break
    print("reply text:", v.replace(chr(10), " | ")[-900:])

    # 检查 AI 对话区的折叠块/按钮/aria
    try:
        info_ = page.evaluate("""() => {
            const out = {buttons: [], aria: []};
            document.querySelectorAll('button, [role=button], [aria-label]').forEach(el => {
                const lbl = el.getAttribute('aria-label') || '';
                const t = (el.innerText || '').trim();
                if (lbl && lbl.length < 60) out.aria.push(lbl);
                if (t && t.length < 40) out.buttons.push(t);
            });
            out.buttons = [...new Set(out.buttons)].slice(0, 50);
            out.aria = [...new Set(out.aria)].slice(0, 50);
            return out;
        }""")
        print("BTNS:", json.dumps(info_.get("buttons"), ensure_ascii=False))
        print("ARIA:", json.dumps(info_.get("aria"), ensure_ascii=False))
    except Exception as e:
        print("scan ERR:", str(e)[:100])

    # 找 thinking/reasoning 关键词的 DOM 元素（折叠块）
    try:
        think_els = page.evaluate("""() => {
            const out = [];
            const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT);
            let node;
            while ((node = walker.nextNode())) {
                const t = (node.innerText || '').trim().toLowerCase();
                if ((t.includes('thinking') || t.includes('thought') || t.includes('reasoning') || t.includes('思维')) && t.length < 100) {
                    out.push({tag: node.tagName, text: t.slice(0, 80), role: node.getAttribute('role') || ''});
                }
            }
            return out.slice(0, 10);
        }""")
        print("THINK ELEMENTS:", json.dumps(think_els, ensure_ascii=False))
    except Exception as e:
        print("think scan ERR:", str(e)[:100])
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()