#!/usr/bin/env python3
"""ws_bind.py — 新建空间后绑定到 user_root（幽灵空间 trap 的解法）：
saveTransactionsMain (space_view set + user_root.space_views listAfter +
space_view_pointers keyedObjectListAfter) → syncRecordValuesMain 轮询确认。

body 依据 create_workspace_capture.json entry 41（2026-07-23 真流）。

用法:
  python ws_bind.py --account <email> --space-id <新空间> [--proxy http://127.0.0.1:3067]
产出: 打印绑定结果; research/logs/ws_bind_runs.jsonl
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
    return json.loads((DETAIL / email / "probe.json").read_text(encoding="utf-8"))


def cookie_header(session_info: dict) -> str:
    parts = []
    for c in session_info.get("cookies", []):
        if c.get("value"):
            parts.append(f'{c["name"]}={c["value"]}')
    if "notion_user_id" not in [c["name"] for c in session_info.get("cookies", [])]:
        parts.append(f"notion_user_id={session_info['user_id']}")
    return "; ".join(parts)


def _new_cookies(session, info=None, prefix=""):
    base_names = {c["name"] for c in (info or {}).get("cookies", [])}
    items = []
    for c in session.cookies.items():
        if c[0] in base_names:
            continue
        items.append(f"{c[0]}={c[1]}")
    return (prefix + "; ".join(items)) if items else ""


def base_headers(session_info: dict, referer: str, space_id: str) -> dict:
    return {
        "User-Agent": UA,
        "Accept": "application/json, text/plain, */*",
        "Accept-Language": "en-US,en;q=0.9",
        "Content-Type": "application/json",
        "origin": APP_HOME,
        "referer": referer,
        "x-notion-active-user-header": session_info["user_id"],
        "x-notion-space-id": space_id,
        "notion-client-version": session_info.get("client_version") or CLIENT_VERSION,
        "notion-audit-log-platform": "web",
        "cookie": cookie_header(session_info),
    }


def build_bind_body(user_id: str, space_id: str) -> dict:
    view_id = str(uuid.uuid4())
    now_ms = int(time.time() * 1000)
    return {
        "requestId": str(uuid.uuid4()),
        "transactions": [{
            "id": str(uuid.uuid4()),
            "spaceId": space_id,
            "debug": {"userAction": "spaceActions.createSpace"},
            "operations": [
                {"pointer": {"table": "space_view", "id": view_id, "spaceId": space_id},
                 "path": [], "command": "set",
                 "args": {"id": view_id, "version": 1, "space_id": space_id,
                          "notify_mobile": True, "notify_desktop": True,
                          "notify_email": True, "parent_id": user_id,
                          "parent_table": "user_root", "alive": True,
                          "first_joined_space_time": now_ms, "joined": True,
                          "settings": {"notify_email_digest": True,
                                       "notify_home_digest_email": True}}},
                {"pointer": {"table": "user_root", "id": user_id},
                 "path": ["space_views"], "command": "listAfter",
                 "args": {"id": view_id}},
                {"pointer": {"table": "user_root", "id": user_id},
                 "path": ["space_view_pointers"], "command": "keyedObjectListAfter",
                 "args": {"value": {"table": "space_view", "id": view_id,
                                    "spaceId": space_id}}},
            ],
        }],
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--space-id", required=True)
    ap.add_argument("--proxy", default="http://127.0.0.1:3067")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    from curl_cffi import requests as cffi

    info = load_probe(args.account)
    s = cffi.Session(impersonate="chrome", proxy=args.proxy, timeout=60)
    hdr = base_headers(info, f"{APP_HOME}/", args.space_id)
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")

    record = {"ts": datetime.now(timezone.utc).isoformat(), "account": args.account,
              "space_id": args.space_id, "proxy": args.proxy}

    body = build_bind_body(info["user_id"], args.space_id)
    t0 = time.time()
    r = s.post(f"{API}/saveTransactionsMain", json=body, headers=hdr)
    dt = round(time.time() - t0, 3)
    if not args.quiet:
        print(f"[saveTransactionsMain] {r.status_code} ({dt}s) {r.text[:300]}")
    record["save"] = {"status": r.status_code, "ms": int(dt * 1000), "body": r.text[:500]}
    if r.status_code != 200:
        LOG_DIR.mkdir(parents=True, exist_ok=True)
        with open(LOG_DIR / "ws_bind_runs.jsonl", "a", encoding="utf-8") as f:
            f.write(json.dumps(record, ensure_ascii=False) + "\n")
        print("BIND_FAIL")
        return
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")

    # syncRecordValuesMain 轮询 user_root.space_view_pointers 确认绑定
    poll_body = {"requests": [
        {"pointer": {"table": "user_root", "id": info["user_id"]}, "version": -1}]}
    ok = False
    for i in range(3):
        t0 = time.time()
        r2 = s.post(f"{API}/syncRecordValuesMain", json=poll_body, headers=hdr)
        dt2 = round(time.time() - t0, 3)
        if not args.quiet:
            print(f"[sync] #{i} {r2.status_code} ({dt2}s)")
        try:
            j = r2.json()
        except Exception:
            j = None
        if isinstance(j, dict):
            ur = j.get("recordMap", {}).get("user_root", {})
            for rid, rv in ur.items():
                v = rv.get("value", {})
                if isinstance(v, dict) and v.get("id") == info["user_id"]:
                    svp = v.get("space_view_pointers", {})
                    has = any(x.get("spaceId") == args.space_id
                              for x in svp.get("value", []))
                    if has:
                        ok = True
                        if not args.quiet:
                            print(f"[bind] CONFIRMED: space_view_pointers 含新空间")
        if ok:
            break
        time.sleep(2)
    record["bound"] = ok
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(LOG_DIR / "ws_bind_runs.jsonl", "a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")
    print("BIND_OK" if ok else "BIND_UNCONFIRMED")


if __name__ == "__main__":
    main()