#!/usr/bin/env python3
"""single_cycle.py — 单次创建全链路验证（贴近真实频率，禁止批量连发）：
validate → createspace → saveTransactionsMain 绑定 → sync 确认 → inference 一条。

用法:
  python single_cycle.py --account <email> [--proxy http://127.0.0.1:3067]
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

UA = ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")


def load(email, name):
    return json.loads((DETAIL / email / name).read_text(encoding="utf-8"))


def cookie_header(info):
    parts = []
    for c in info.get("cookies", []):
        if c.get("value"):
            parts.append(f'{c["name"]}={c["value"]}')
    if "notion_user_id" not in [c["name"] for c in info.get("cookies", [])]:
        parts.append(f"notion_user_id={info['user_id']}")
    return "; ".join(parts)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--proxy", default="http://127.0.0.1:3067")
    args = ap.parse_args()

    from curl_cffi import requests as cffi

    info = load(args.account, "probe.json")
    acc = load(args.account, "account.json")
    device_id = acc.get("device_id") or str(uuid.uuid4())
    s = cffi.Session(impersonate="chrome", proxy=args.proxy, timeout=120)
    hdr = {
        "User-Agent": UA,
        "Accept": "application/json, text/plain, */*",
        "Accept-Language": "en-US,en;q=0.9",
        "Content-Type": "application/json",
        "origin": APP_HOME,
        "referer": f"{APP_HOME}/onboarding",
        "x-notion-active-user-header": info["user_id"],
        "x-notion-space-id": info["space_id"],
        "notion-client-version": info.get("client_version") or "23.13.20260720.1949",
        "notion-audit-log-platform": "web",
        "cookie": cookie_header(info),
    }
    print(f"[{args.account}] tier={acc.get('tier')} device={device_id[:16]}.. sid={info['space_id'][:12]}")

    # 1) validate
    r = s.post(f"{API}/validateusercancreateworkspace", json={}, headers=hdr, timeout=60)
    print(f"[validate] {r.status_code} {r.text[:120]}")
    if r.status_code != 200:
        print("STOP: validate failed")
        return

    # 2) createspace（单次）
    body = {
        "name": f"cycle-{uuid.uuid4().hex[:6]}'s Space",
        "planType": "personal", "planSelection": "personal",
        "initialPersona": "unfilled",
        "deviceId": device_id, "deviceType": "web-desktop",
        "source": "handle_root_redirect",
    }
    t0 = time.time()
    r = s.post(f"{API}/createspace", json=body, headers=hdr, timeout=120)
    dt = round(time.time() - t0, 3)
    print(f"[createspace] {r.status_code} ({dt}s) {r.text[:250]}")
    if r.status_code != 200:
        print("STOP: createspace failed")
        return
    j = r.json()
    space_id = j.get("spaceId", "")
    print(f"[createspace] NEW_SPACE={space_id}")

    # 3) 绑定
    view_id = str(uuid.uuid4())
    now_ms = int(time.time() * 1000)
    bind = {
        "requestId": str(uuid.uuid4()),
        "transactions": [{
            "id": str(uuid.uuid4()), "spaceId": space_id,
            "debug": {"userAction": "spaceActions.createSpace"},
            "operations": [
                {"pointer": {"table": "space_view", "id": view_id, "spaceId": space_id},
                 "path": [], "command": "set",
                 "args": {"id": view_id, "version": 1, "space_id": space_id,
                          "notify_mobile": True, "notify_desktop": True, "notify_email": True,
                          "parent_id": info["user_id"], "parent_table": "user_root",
                          "alive": True, "first_joined_space_time": now_ms, "joined": True,
                          "settings": {"notify_email_digest": True, "notify_home_digest_email": True}}},
                {"pointer": {"table": "user_root", "id": info["user_id"]},
                 "path": ["space_views"], "command": "listAfter", "args": {"id": view_id}},
                {"pointer": {"table": "user_root", "id": info["user_id"]},
                 "path": ["space_view_pointers"], "command": "keyedObjectListAfter",
                 "args": {"value": {"table": "space_view", "id": view_id, "spaceId": space_id}}},
            ],
        }],
    }
    hdr["referer"] = f"{APP_HOME}/"
    r = s.post(f"{API}/saveTransactionsMain", json=bind, headers=hdr, timeout=60)
    print(f"[bind save] {r.status_code} {r.text[:120]}")
    if r.status_code != 200:
        print("STOP: bind failed")
        return
    poll = {"requests": [{"pointer": {"table": "user_root", "id": info["user_id"]}, "version": -1}]}
    bound = False
    for i in range(6):
        r2 = s.post(f"{API}/syncRecordValuesMain", json=poll, headers=hdr, timeout=30)
        try:
            ur = r2.json().get("recordMap", {}).get("user_root", {})
            for rid, rv in ur.items():
                v = rv.get("value", {})
                if isinstance(v, dict) and v.get("id") == info["user_id"]:
                    svp = v.get("space_view_pointers", {}).get("value", [])
                    if any(x.get("spaceId") == space_id for x in svp):
                        bound = True
        except Exception:
            pass
        if bound:
            break
        time.sleep(1.5)
    print(f"[bind confirm] bound={bound}")

    # 4) inference 一条（新空间额度）
    now_ms2 = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    hdr2 = dict(hdr)
    hdr2["x-notion-space-id"] = space_id
    hdr2["referer"] = f"{APP_HOME}/"
    transcript = [
        {"id": str(uuid.uuid4()), "type": "config",
         "value": {"type": "workflow", "useWebSearch": False, "useReadOnlyMode": False,
                   "modelFromUser": False, "enableAgentGenerateImage": False,
                   "availableConnectors": [], "customConnectorInfo": [], "searchScopes": []}},
        {"id": str(uuid.uuid4()), "type": "context",
         "value": {"timezone": "Asia/Shanghai", "userName": acc.get("user_name", ""),
                   "userId": info["user_id"], "userEmail": info["email"],
                   "spaceName": "cycle", "spaceId": space_id,
                   "currentDatetime": now_ms2, "surface": "ai_module"}},
        {"id": str(uuid.uuid4()), "type": "user",
         "value": [["Reply with exactly: OK"]],
         "userId": info["user_id"], "createdAt": now_ms2},
    ]
    inf_body = {"spaceId": space_id, "threadId": str(uuid.uuid4()),
                "createThread": True, "generateTitle": False, "traceId": str(uuid.uuid4()),
                "transcript": transcript, "threadType": "workflow", "asPatchResponse": True,
                "isPartialTranscript": False, "saveAllThreadOperations": False,
                "setUnreadState": True, "createdSource": "ai_module",
                "isUserInAnySalesAssistedSpace": False, "isSpaceSalesAssisted": False,
                "debugOverrides": {"annotationInferences": {}, "cachedInferences": {},
                                   "emitAgentSearchExtractedResults": True, "emitInferences": False},
                "threadParentPointer": {"table": "space", "id": space_id, "spaceId": space_id}}
    r = s.post(f"{API}/runInferenceTranscript", json=inf_body, headers=hdr2, timeout=120)
    print(f"[inference] {r.status_code} bytes={len(r.text)} head={r.text[:120]!r}")

    record = {"ts": datetime.now(timezone.utc).isoformat(), "account": args.account,
              "space_id": space_id, "bound": bound, "inference_status": r.status_code}
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(LOG_DIR / "single_cycle_runs.jsonl", "a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")
    print("CYCLE_RESULT:", "ALL_OK" if bound and r.status_code == 200 else "PARTIAL")


if __name__ == "__main__":
    main()