#!/usr/bin/env python3
"""ws_create.py — 研究 spike #1：纯 HTTP 在已有账号内新建工作空间（轮换引擎的地基）。

依据 create_workspace_capture.json（2026-07-23）抓到的真 body：
  validateusercancreateworkspace {} → createspace {name, planType, planSelection,
  initialPersona, deviceId, deviceType, source} → {spaceId,...}

用法:
  python ws_create.py --account <email> [--plan personal|team] [--proxy http://127.0.0.1:3067]
产出: 打印新 space_id; 运行日志追加 research/logs/ws_create_runs.jsonl
"""
from __future__ import annotations
import argparse, json, time, uuid, pathlib, sys
from datetime import datetime, timezone

if sys.stdout and hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")

APP_HOME = "https://app.notion.com"
API = f"{APP_HOME}/api/v3"
REG_ROOT = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register")
DETAIL = REG_ROOT / "accounts" / "detail"
LOG_DIR = pathlib.Path(__file__).parent / "logs"
CLIENT_VERSION = "23.13.20260720.1949"

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")


def load_probe(email: str) -> dict:
    p = json.loads((DETAIL / email / "probe.json").read_text(encoding="utf-8"))
    return p


def cookie_header(session_info: dict) -> str:
    parts = []
    for c in session_info.get("cookies", []):
        if c.get("value"):
            parts.append(f'{c["name"]}={c["value"]}')
    # notion_user_id 兜底
    if "notion_user_id" not in [c["name"] for c in session_info.get("cookies", [])]:
        parts.append(f"notion_user_id={session_info['user_id']}")
    return "; ".join(parts)


def base_headers(session_info: dict, referer: str) -> dict:
    return {
        "User-Agent": UA,
        "Accept": "application/json, text/plain, */*",
        "Accept-Language": "en-US,en;q=0.9",
        "Content-Type": "application/json",
        "origin": APP_HOME,
        "referer": referer,
        "x-notion-active-user-header": session_info["user_id"],
        "x-notion-space-id": session_info["space_id"],
        "notion-client-version": session_info.get("client_version") or CLIENT_VERSION,
        "notion-audit-log-platform": "web",
        "cookie": cookie_header(session_info),
    }


def _new_cookies(session, info=None, prefix=""):
    """吸收响应 Set-Cookie 里 probe 没有的新 cookie（如刷新后的 __cf_bm），追加进 cookie 头。"""
    base_names = {c["name"] for c in (info or {}).get("cookies", [])}
    items = []
    for c in session.cookies.items():
        if c[0] in base_names:
            continue
        items.append(f"{c[0]}={c[1]}")
    return (prefix + "; ".join(items)) if items else ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--plan", default="personal", choices=["personal", "team"])
    ap.add_argument("--proxy", default="http://127.0.0.1:3067")
    ap.add_argument("--device-id", default="", help="固定 deviceId；空则读 account.json 的 device_id")
    ap.add_argument("--preflight", action="store_true", help="按 capture 序先 getJoinableSpaces 再 validate 再 createspace")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    from curl_cffi import requests as cffi

    info = load_probe(args.account)
    acc = json.loads((DETAIL / args.account / "account.json").read_text(encoding="utf-8"))
    device_id = args.device_id or acc.get("device_id") or str(uuid.uuid4())
    if args.quiet is False:
        print(f"[{args.account}] probe: sid={info['space_id']} cv={info.get('client_version')} cookies={len(info.get('cookies', []))}")
        print(f"[proxy] {args.proxy}")
        print(f"[device_id] {device_id}")

    s = cffi.Session(impersonate="chrome", proxy=args.proxy, timeout=120)

    # 预热：GET 一次 app.notion.com，吸收 Set-Cookie（含 __cf_bm 刷新）
    try:
        w = s.get(f"{APP_HOME}/home", headers={"User-Agent": UA, "cookie": cookie_header(info)})
        if w.status_code == 301:
            w = s.get(f"{APP_HOME}/", headers={"User-Agent": UA, "cookie": cookie_header(info) + "; " + _new_cookies(s, info=info)})
        if args.quiet is False:
            print(f"[warmup] GET {APP_HOME} status={w.status_code}")
    except Exception as e:
        print(f"[warmup] failed: {e}")

    hdr = base_headers(info, f"{APP_HOME}/onboarding")
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")

    # 0) 可选 preflight：getJoinableSpaces（capture 序：joinable 在 validate 前）
    if args.preflight:
        t0 = time.time()
        r0 = s.post(f"{API}/getJoinableSpaces", json={"excludeUnactionableSpaces": False}, headers=hdr)
        if args.quiet is False:
            j0 = r0.text[:300]
            print(f"[joinable] {r0.status_code} ({round(time.time() - t0, 3)}s) {j0}")
        hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")

    # 1) validate
    t0 = time.time()
    r1 = s.post(f"{API}/validateusercancreateworkspace", json={}, headers=hdr)
    dt1 = round(time.time() - t0, 3)
    if args.quiet is False:
        print(f"[validate] {r1.status_code} ({dt1}s) {r1.text[:150]}")
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")

    # 2) createspace (超时/非200 自动重试最高 3 次)
    body = {
        "name": "spikeprobe's Space",
        "planType": args.plan,
        "planSelection": args.plan,
        "initialPersona": "unfilled",
        "deviceId": device_id,
        "deviceType": "web-desktop",
        "source": "handle_root_redirect",
    }
    r2 = None
    dt2 = 0.0
    for attempt in range(1, 4):
        t0 = time.time()
        try:
            r2 = s.post(f"{API}/createspace", json=body, headers=hdr)
            dt2 = round(time.time() - t0, 3)
            if args.quiet is False:
                print(f"[createspace] attempt{attempt} {r2.status_code} ({dt2}s)")
            if r2.status_code < 500 and attempt > 1:
                break
            if r2.status_code < 500:
                break
        except Exception as e:
            dt2 = round(time.time() - t0, 3)
            if args.quiet is False:
                print(f"[createspace] attempt{attempt} ERR {dt2}s: {type(e).__name__} {str(e)[:160]}")
            time.sleep(2)
            if not (args.device_id or acc.get("device_id")):
                body["deviceId"] = str(uuid.uuid4())  # 无固定 deviceId 时换新再试
            continue
        hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
    if r2 is None:
        r2 = type("R", (), {"status_code": 0, "text": "no-response"})()

    text = r2.text[:2000]
    new_space_id = ""
    try:
        j = r2.json()
        new_space_id = (j.get("spaceId") or "")
    except Exception:
        j = None

    record = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "account": args.account,
        "plan": args.plan,
        "proxy": args.proxy,
        "device_id": device_id,
        "validate_status": r1.status_code, "validate_ms": int(dt1 * 1000),
        "validate_body": r1.text[:300],
        "create_status": r2.status_code, "create_ms": int(dt2 * 1000),
        "new_space_id": new_space_id,
        "create_body_short": text[:500],
    }
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(LOG_DIR / "ws_create_runs.jsonl", "a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")

    print(f"validate: {r1.status_code} ({dt1}s)  body={r1.text[:200]}")
    print(f"createspace: {r2.status_code} ({dt2}s)")
    print("resp:", text)
    print("NEW_SPACE_ID:", new_space_id)


if __name__ == "__main__":
    main()