#!/usr/bin/env python3
"""ws_loop.py — 同账号 create×N 高频实测（spike #1 剩余项）：
循环 N 次 { validate → createspace → saveTransactionsMain 绑定 → sync 确认 }，
记录每次状态码/耗时/绑定结果，看 429/冷却/绑定失败率/换 deviceId 是否复位。

用法:
  python ws_loop.py --account <email> --n 5 [--plan personal] [--gap 3]
                    [--rotate-device] [--proxy http://127.0.0.1:3067]
产出: research/logs/ws_loop_runs.jsonl
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


def bind_space(s, info, hdr, space_id: str) -> bool:
    view_id = str(uuid.uuid4())
    now_ms = int(time.time() * 1000)
    body = {
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
                          "notify_email": True, "parent_id": info["user_id"],
                          "parent_table": "user_root", "alive": True,
                          "first_joined_space_time": now_ms, "joined": True,
                          "settings": {"notify_email_digest": True,
                                       "notify_home_digest_email": True}}},
                {"pointer": {"table": "user_root", "id": info["user_id"]},
                 "path": ["space_views"], "command": "listAfter",
                 "args": {"id": view_id}},
                {"pointer": {"table": "user_root", "id": info["user_id"]},
                 "path": ["space_view_pointers"], "command": "keyedObjectListAfter",
                 "args": {"value": {"table": "space_view", "id": view_id,
                                    "spaceId": space_id}}},
            ],
        }],
    }
    r = s.post(f"{API}/saveTransactionsMain", json=body, headers=hdr, timeout=60)
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
    if r.status_code != 200:
        return False
    poll = {"requests": [{"pointer": {"table": "user_root", "id": info["user_id"]}, "version": -1}]}
    for _ in range(3):
        r2 = s.post(f"{API}/syncRecordValuesMain", json=poll, headers=hdr, timeout=30)
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
                    if any(x.get("spaceId") == space_id for x in svp.get("value", [])):
                        return True
        time.sleep(1.5)
    return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--n", type=int, default=5)
    ap.add_argument("--plan", default="personal", choices=["personal", "team"])
    ap.add_argument("--gap", type=float, default=3.0, help="每轮间隔秒")
    ap.add_argument("--rotate-device", action="store_true", help="每轮换新 deviceId")
    ap.add_argument("--proxy", default="http://127.0.0.1:3067")
    args = ap.parse_args()

    from curl_cffi import requests as cffi

    info = load_probe(args.account)
    acc = json.loads((DETAIL / args.account / "account.json").read_text(encoding="utf-8"))
    device_id = acc.get("device_id") or str(uuid.uuid4())
    print(f"[{args.account}] user={info['user_id'][:16]}.. device={device_id[:16]}.. "
          f"plan={args.plan} n={args.n} rotate_device={args.rotate_device}")

    s = cffi.Session(impersonate="chrome", proxy=args.proxy, timeout=120)
    hdr = base_headers(info, f"{APP_HOME}/onboarding", info["space_id"])
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
    try:
        s.get(f"{APP_HOME}/home", headers={"User-Agent": UA, "cookie": cookie_header(info)}, timeout=30)
        hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
    except Exception:
        pass

    runs = []
    for i in range(1, args.n + 1):
        did = str(uuid.uuid4()) if args.rotate_device else device_id
        t0 = time.time()
        r1 = s.post(f"{API}/validateusercancreateworkspace", json={}, headers=hdr, timeout=60)
        dt1 = round(time.time() - t0, 3)
        hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
        body = {
            "name": f"loop{i}'s Space",
            "planType": args.plan,
            "planSelection": args.plan,
            "initialPersona": "unfilled",
            "deviceId": did,
            "deviceType": "web-desktop",
            "source": "handle_root_redirect",
        }
        t0 = time.time()
        try:
            r2 = s.post(f"{API}/createspace", json=body, headers=hdr, timeout=120)
            dt2 = round(time.time() - t0, 3)
            new_sid = ""
            try:
                j = r2.json()
                new_sid = j.get("spaceId", "")
            except Exception:
                j = None
                new_sid = ""
        except Exception as e:
            dt2 = round(time.time() - t0, 3)
            r2 = type("R", (), {"status_code": 0})()
            new_sid = ""
            print(f"  [#{i}] createspace EXC {type(e).__name__} {str(e)[:120]}")
        hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
        bound = False
        if r2.status_code == 200 and new_sid:
            bound = bind_space(s, info, hdr, new_sid)
        run = {"i": i, "ts": datetime.now(timezone.utc).isoformat(),
               "validate": r1.status_code, "validate_ms": int(dt1 * 1000),
               "create": r2.status_code, "create_ms": int(dt2 * 1000),
               "space_id": new_sid, "bound": bound,
               "device_id": did}
        runs.append(run)
        print(f"  [#{i}] validate={r1.status_code} create={r2.status_code}({dt2}s) "
              f"space={new_sid[:12] if new_sid else 'NONE'} bound={bound}")
        if i < args.n:
            time.sleep(args.gap)

    LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(LOG_DIR / "ws_loop_runs.jsonl", "a", encoding="utf-8") as f:
        f.write(json.dumps({"ts": datetime.now(timezone.utc).isoformat(),
                            "account": args.account, "plan": args.plan,
                            "n": args.n, "gap": args.gap,
                            "rotate_device": args.rotate_device,
                            "runs": runs}, ensure_ascii=False) + "\n")
    ok = sum(1 for r in runs if r["create"] == 200 and r["bound"])
    print(f"SUMMARY: {ok}/{len(runs)} 成功建+绑; 429/5xx: "
          f"{sum(1 for r in runs if r['create'] in (429, 500, 502, 503, 504))}")


if __name__ == "__main__":
    main()