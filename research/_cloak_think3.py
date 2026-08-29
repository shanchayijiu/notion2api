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
    for _ in range(20):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,300):''")
        if "How can I help you today" in v or "Personalize" in v:
            break
    print("url:", page.url)

    # 模型选择：点 Auto 打开模型菜单，选 thinking 模型（opus）
    try:
        loc = page.locator('text=Auto').first
        if loc.count():
            loc.click(timeout=5000)
            print("opened model menu")
            time.sleep(2)
            v = page.evaluate("()=>document.body?document.body.innerText.slice(0,1500):''")
            print("MODEL MENU:", v.replace(chr(10), " | ")[-800:])
            # 选 opus
            for m in ["Opus 5", "Opus", "GPT-5.6"]:
                try:
                    l2 = page.locator(f'text={m}').first
                    if l2.count():
                        l2.click(timeout=4000)
                        print("selected", m)
                        break
                except Exception:
                    pass
            time.sleep(2)
    except Exception as e:
        print("model menu ERR:", str(e)[:120])

    # 发 thinking 题
    try:
        ed = page.locator('[contenteditable="true"]').first
        ed.click(timeout=5000)
        ed.fill("Solve: if a train leaves at 3pm going 60mph and another at 4pm going 90mph, when do they meet? Walk through your full thought process.")
        time.sleep(1)
        page.keyboard.press("Enter")
        print("sent")
    except Exception as e:
        print("send ERR:", str(e)[:120])

    # 等回复 + DOM 元素级扫描（找折叠块/图标/展开按钮）
    for i in range(30):
        time.sleep(3)
        v = page.evaluate("()=>document.body?document.body.innerText.slice(0,3000):''")
        if "meet" in v and ("6pm" in v or "6:00" in v or "4pm" in v) and i > 5:
            break
    print("reply:", v.replace(chr(10), " | ")[-600:])

    # DOM 扫描：对话区所有按钮/图标/aria-expanded（thinking 折叠块线索）
    dom = page.evaluate("""() => {
        const out = {expandable: [], icons: [], aria: []};
        document.querySelectorAll('[aria-expanded], [role=button], button, [role=switch], summary, details').forEach(el => {
            const a = el.getAttribute('aria-label') || '';
            const t = (el.innerText || '').trim();
            const exp = el.getAttribute('aria-expanded');
            if (a) out.aria.push(a.slice(0, 50));
            if (exp !== null) out.expandable.push({a: a.slice(0, 40), exp});
            if (t && t.length < 30) out.icons.push(t);
        });
        out.aria = [...new Set(out.aria)].slice(0, 30);
        out.icons = [...new Set(out.icons)].slice(0, 30);
        return out;
    }""")
    print("EXPANDABLE:", json.dumps(dom.get("expandable"), ensure_ascii=False))
    print("ICONS/BTNS:", json.dumps(dom.get("icons"), ensure_ascii=False))
    print("ARIA:", json.dumps(dom.get("aria"), ensure_ascii=False))

    # 对话区完整文本（含可能隐藏的块）
    full = page.evaluate("""() => {
        const out = [];
        document.querySelectorAll('[data-testid], [class*="message"], [class*="assistant"], [class*="ai"]').forEach(el => {
            const t = (el.innerText || '').trim();
            if (t && t.length > 20 && t.length < 1500) out.push(t.slice(0, 500));
        });
        return out.slice(-8);
    }""")
    print("DIALOG BLOCKS:")
    for b in full:
        print("  |", b.replace(chr(10), " / ")[:300])
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()