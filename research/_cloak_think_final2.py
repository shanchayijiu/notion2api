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

    # 找模型选择器（Auto 按钮的 DOM 结构）
    info_ = page.evaluate("""() => {
        const els = Array.from(document.querySelectorAll('[aria-label], [role=button], button, [data-testid]'));
        return els.filter(el => {
            const t = (el.innerText || '').trim();
            const a = el.getAttribute('aria-label') || '';
            return t === 'Auto' || a.includes('model') || a.includes('Model') || t.includes('Auto');
        }).slice(0, 5).map(el => ({
            tag: el.tagName, aria: el.getAttribute('aria-label'),
            testid: el.getAttribute('data-testid'),
            text: (el.innerText || '').trim().slice(0, 30),
            cls: (el.className || '').toString().slice(0, 60)
        }));
    }""")
    print("MODEL SELECTOR:", json.dumps(info_, ensure_ascii=False, indent=1))

    # 点击模型选择器（Auto）
    clicked = page.evaluate("""() => {
        const els = Array.from(document.querySelectorAll('[role=button], button, [aria-label]'));
        for (const el of els) {
            const t = (el.innerText || '').trim();
            if (t === 'Auto' || (el.getAttribute('aria-label') || '').toLowerCase().includes('model')) {
                el.click();
                return {clicked: true, tag: el.tagName, aria: el.getAttribute('aria-label')};
            }
        }
        return {clicked: false};
    }""")
    print("CLICK:", clicked)
    time.sleep(3)
    v = page.evaluate("()=>document.body?document.body.innerText.slice(0,1500):''")
    print("menu:", v.replace(chr(10), " | ")[-700:])

    # 选 Opus/Claude 模型
    for m in ["Opus 5", "Opus", "Claude Opus"]:
        try:
            loc = page.locator(f'text={m}').first
            if loc.count():
                loc.click(timeout=4000)
                print("selected:", m)
                break
        except Exception:
            pass
    time.sleep(2)

    ed = page.locator('[contenteditable="true"]').first
    ed.click(timeout=5000)
    ed.fill("What is 17 times 23? Think step by step and show your reasoning process.")
    time.sleep(1)
    page.keyboard.press("Enter")
    print("sent")

    for i in range(40):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,4000):''")
        if ("Thinking" in v or "Thought" in v) and "391" in v:
            print("THOUGHT + ANSWER at t", (i+1)*3)
            break
    time.sleep(4)

    # 抓 Thought 块
    result = page.evaluate("""() => {
        const out = [];
        const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT);
        let node;
        while ((node = walker.nextNode())) {
            const t = (node.innerText || '').trim();
            if (t === 'Thought' || t === 'Thinking' || t === 'Thinking…' || t === 'Thinking...') {
                let cur = node.parentElement;
                for (let d = 0; cur && d < 10; cur = cur.parentElement, d++) {
                    const ct = (cur.innerText || '').trim();
                    if (ct.length > 200 && (ct.includes('Thought') || ct.includes('Thinking'))) {
                        out.push(ct);
                        break;
                    }
                }
                break;
            }
        }
        return out;
    }""")
    print("THOUGHT COUNT:", len(result))
    if result:
        print("THOUGHT CONTENT:", result[0][:1800])
        pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\ui_thought_full.txt").write_text(result[0], encoding="utf-8")
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()