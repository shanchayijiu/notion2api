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
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,300):''")
        if "How can I help you today" in v or "Personalize" in v:
            break
    print("url:", page.url)

    ed = page.locator('[contenteditable="true"]').first
    ed.click(timeout=5000)
    ed.fill("A clock shows 3:15. What is the angle between the hour and minute hands? Think through the calculation step by step.")
    time.sleep(1)
    page.keyboard.press("Enter")
    print("sent")

    thought_found = False
    for i in range(30):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,4000):''")
        if "Thought" in v and "52.5" in v:
            thought_found = True
            print("Thought + answer at t", (i+1)*3)
            break
    time.sleep(4)

    # DOM 抓取：找 Thought 块（元素级定位 + 内容）
    result = page.evaluate("""() => {
        // 找所有含 "Thought" 文本的块级元素
        const out = [];
        const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT);
        let node;
        while ((node = walker.nextNode())) {
            const t = (node.innerText || '').trim();
            // Thought 标题元素（短文本）
            if (t === 'Thought' || t === 'Thinking' || t === 'Thinking…' || t === 'Thinking...') {
                // 向上找对话块容器
                let cur = node.parentElement;
                let container = null;
                for (let d = 0; cur && d < 8; cur = cur.parentElement, d++) {
                    const ct = (cur.innerText || '').trim();
                    if (ct.length > 200 && ct.includes('Thought')) { container = cur; break; }
                }
                out.push({
                    label: t,
                    labelTag: node.tagName,
                    containerText: container ? container.innerText.slice(0, 3000) : ''
                });
                break;
            }
        }
        return out;
    }""")
    print("THOUGHT BLOCKS:", len(result))
    for r in result:
        print(" label:", r.get("label"), "tag:", r.get("labelTag"))
        print(" container:", r.get("containerText", "").replace(chr(10), " | ")[:1200])

    # 保存完整对话文本（含 thinking）
    full = page.evaluate("()=>document.body?document.body.innerText.slice(0,5000):''")
    out = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\ui_thought_capture.txt")
    out.write_text(full, encoding="utf-8")
    print("saved dialog text:", out)
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()