#!/usr/bin/env python3
"""cloak_agent.py — Cloak 浏览器通道：对话 + 抓 UI thinking（前端直接显示，无需解密）

流程: 登录 → /ai → 选模型(默认 Opus 5) → 发消息 → 等回复 → DOM 抓 Thought 块(thinking)
     + 正文 → 输出 JSON {reasoning, content, model}

用法:
  python cloak_agent.py --account <email> --prompt "<问题>" [--model "Opus 5"]
  python cloak_agent.py --account <email> --file <prompt.txt> --json
"""
from __future__ import annotations
import argparse, json, pathlib, sys, time
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from cloakbrowser import launch

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
PROXY = "http://127.0.0.1:7890"


def load_cookies(email: str):
    info = json.loads((DETAIL / email / "probe.json").read_text(encoding="utf-8"))
    cookies, seen = [], set()
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
    return info, cookies


def run_dialog(email: str, prompt: str, model: str = "Opus 5", timeout_s: int = 180) -> dict:
    info, cookies = load_cookies(email)
    browser = launch(headless=True, proxy=PROXY, humanize=True, locale="en-US")
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
            v = page.evaluate("()=>document.body?document.body.innerText.slice(0,200):''")
            if "How can I help you today" in v or "Personalize" in v:
                break

        # 选模型
        try:
            page.evaluate("""() => {
                const els = Array.from(document.querySelectorAll('[role=button], button, [aria-label]'));
                for (const el of els) {
                    const t = (el.innerText || '').trim();
                    if (t === 'Auto' || (el.getAttribute('data-testid') || '').includes('model')) { el.click(); return; }
                }
            }""")
            time.sleep(2)
            loc = page.locator(f'text={model}').first
            if loc.count():
                loc.click(timeout=4000)
        except Exception:
            pass
        time.sleep(1)
        try:
            page.keyboard.press("Escape")
        except Exception:
            pass
        time.sleep(2)

        # 发消息
        ed = page.locator('[contenteditable="true"]').first
        ed.click(timeout=5000)
        ed.fill(prompt)
        time.sleep(1)
        page.keyboard.press("Enter")
        print("[debug] sent", flush=True)

        # 等回复完成：问题文本出现后回复稳定 30s（无 finished 标志依赖）
        done = False
        prompt_seen = False
        stable_tail = ""
        stable_count = 0
        for i in range(timeout_s // 3):
            time.sleep(3)
            v = page.evaluate("()=>document.body?document.body.innerText.slice(0,6000):''")
            if prompt in v:
                prompt_seen = True
            if prompt_seen and "Oops" in v or "Something went wrong" in v:
                return {"ok": False, "error": "upstream error in dialog"}
            if prompt_seen:
                tail = v.replace(chr(10), " | ")[-300:]
                if tail == stable_tail:
                    stable_count += 1
                    if stable_count >= 10:
                        done = True
                        break
                else:
                    stable_tail = tail
                    stable_count = 0
            if i % 10 == 0:
                print(f"[debug] t{(i+1)*3}s tail:", v.replace(chr(10), " | ")[-150:], flush=True)
        if not done:
            return {"ok": False, "error": "timeout waiting for reply"}

        # DOM 抓 thinking（Thought/Thinking 块）+ 正文
        result = page.evaluate("""() => {
            const out = {thought: '', answer: ''};
            const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_ELEMENT);
            let node;
            while ((node = walker.nextNode())) {
                const t = (node.innerText || '').trim();
                if (t === 'Thought' || t === 'Thinking' || t === 'Thinking…' || t === 'Thinking...') {
                    let cur = node.parentElement;
                    for (let d = 0; cur && d < 10; cur = cur.parentElement, d++) {
                        const ct = (cur.innerText || '').trim();
                        if (ct.length > 150 && (ct.includes('Thought') || ct.includes('Thinking'))) {
                            // 去掉标签行，取 thinking 内容
                            const lines = ct.split('\\n');
                            const idx = lines.findIndex(l => l.trim() === 'Thought' || l.trim() === 'Thinking' || l.trim() === 'Thinking…');
                            if (idx >= 0) out.thought = lines.slice(idx + 1).join('\\n').trim();
                            break;
                        }
                    }
                    break;
                }
            }
            // 正文 = 最后一段（含 Answer/Notion AI finished 前的 AI 回复）
            const body = document.body.innerText;
            const parts = body.split('Notion AI finished');
            if (parts.length > 1) out.answer = parts[parts.length - 2].split('\\n').slice(-40).join('\\n').trim();
            return out;
        }""")
        return {"ok": True, "reasoning": result.get("thought", ""), "content": result.get("answer", "")}
    finally:
        browser.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--prompt", default="")
    ap.add_argument("--file", default="")
    ap.add_argument("--model", default="Opus 5")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()
    prompt = args.prompt
    if args.file:
        prompt = pathlib.Path(args.file).read_text(encoding="utf-8")
    if not prompt:
        print("prompt required"); sys.exit(1)
    out = run_dialog(args.account, prompt, args.model)
    if args.json:
        print(json.dumps(out, ensure_ascii=False))
    else:
        print("OK:", out.get("ok"))
        print("=== reasoning (thinking) ===")
        print(out.get("reasoning", "")[:1500])
        print("=== content ===")
        print(out.get("content", "")[:1000])
        if not out.get("ok"):
            print("ERROR:", out.get("error"))
            sys.exit(1)


if __name__ == "__main__":
    main()