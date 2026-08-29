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

def click_text(page, text):
    try:
        loc = page.locator(f'text={text}')
        if loc.count():
            loc.first.click(timeout=5000)
            print(f"clicked: {text}")
            return True
    except Exception:
        pass
    return False

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

    # 1) 打开 Settings（AI 设置）
    try:
        loc = page.get_by_role("button", name="Settings")
        if loc.count():
            loc.first.click(timeout=5000)
            print("opened Settings")
            time.sleep(3)
            print("SETTINGS BODY:", vis(page).replace(chr(10), " | ")[-1200:])
    except Exception as e:
        print("settings ERR:", str(e)[:120])

    # 2) 打开 Personalize（个性化设置）
    click_text(page, "Personalize")
    time.sleep(3)
    print("PERSONALIZE BODY:", vis(page).replace(chr(10), " | ")[-1200:])

    # 3) 模型选择器（Auto）— 找 thinking 相关选项
    try:
        loc = page.locator('[aria-label*="model"], [data-testid*="model"], text=Auto')
        n = loc.count()
        print("model selector count:", n)
        if n:
            loc.first.click(timeout=5000)
            time.sleep(2)
            v = vis(page)
            print("MODEL MENU:", v.replace(chr(10), " | ")[-900:])
    except Exception as e:
        print("model ERR:", str(e)[:100])

    # 4) 扫描所有按钮/菜单项（找 reasoning/thinking 字样）
    info_ = page.evaluate("""() => {
        const out = [];
        document.querySelectorAll('button, [role=button], [role=menuitem], [role=switch], [role=checkbox], [role=option]').forEach(el => {
            const t = (el.innerText || '').trim();
            if (t && t.length < 60) out.push(t);
        });
        return [...new Set(out)].slice(0, 60);
    }""")
    print("ALL MENU ITEMS:", json.dumps(info_, ensure_ascii=False))
except Exception as e:
    print("ERR:", str(e)[:200])
browser.close()