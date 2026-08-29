#!/usr/bin/env python3
"""ws_verify.py — 验证新建工作空间是否真正可用：
1) getSpacesInitial 是否包含新空间（幽灵空间判定）
2) runInferenceTranscript 直发一条（额度是否随新空间重置 + 返回形态）

用法:
  python ws_verify.py --account <email> [--space-id <新空间>] [--proxy http://127.0.0.1:3067]
产出: research/logs/ws_verify_runs.jsonl
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--space-id", default="", help="要验证的新空间；空则用 probe 的 space_id")
    ap.add_argument("--proxy", default="http://127.0.0.1:3067")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()

    from curl_cffi import requests as cffi

    info = load_probe(args.account)
    s = cffi.Session(impersonate="chrome", proxy=args.proxy, timeout=60)
    hdr = base_headers(info, f"{APP_HOME}/", args.space_id or info["space_id"])
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")

    record = {"ts": datetime.now(timezone.utc).isoformat(), "account": args.account,
              "space_id": args.space_id or info["space_id"], "proxy": args.proxy}

    # 1) getSpacesInitial — 新空间在不在账号空间列表
    t0 = time.time()
    r = s.post(f"{API}/getSpacesInitial", json={}, headers=hdr)
    dt = round(time.time() - t0, 3)
    j = {}
    try:
        j = r.json()
    except Exception:
        pass
    spaces = j.get("spaces", {}) if isinstance(j, dict) else {}
    in_list = args.space_id in spaces if args.space_id else bool(spaces)
    if not args.quiet:
        print(f"[getSpacesInitial] {r.status_code} ({dt}s) spaces={len(spaces)} "
              f"target_in_list={in_list}")
    record["spaces_initial"] = {"status": r.status_code, "ms": int(dt * 1000),
                                "n_spaces": len(spaces), "target_in_list": in_list,
                                "space_names": [sp.get("value", {}).get("name", "")[:20]
                                                for sp in list(spaces.values())[:10]]}

    # 2) runInferenceTranscript — 新空间额度直发（GALIAIS 格式 body）
    now_ms = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    config_id = str(uuid.uuid4())
    context_id = str(uuid.uuid4())
    user_step_id = str(uuid.uuid4())
    transcript = [
        {"id": config_id, "type": "config",
         "value": {"type": "workflow", "useWebSearch": False, "useSearchToolV2": False,
                   "useReadOnlyMode": True, "writerMode": False, "modelFromUser": False,
                   "enableAgentGenerateImage": False, "enableAgentDiffs": True,
                   "enableAgentAutomations": True, "enableAgentIntegrations": True,
                   "enableCustomAgents": True, "enableScriptAgent": True,
                   "enableMailExplicitToolCalls": True, "enableScriptAgentSlack": True,
                   "enableScriptAgentMail": True, "enableScriptAgentCalendar": True,
                   "enableCreateAndRunThread": True, "availableConnectors": [],
                   "customConnectorInfo": [], "searchScopes": []}},
        {"id": context_id, "type": "context",
         "value": {"timezone": "Asia/Shanghai", "userName": info.get("user_name", ""),
                   "userId": info["user_id"], "userEmail": info["email"],
                   "spaceName": "spikeprobe's Space", "spaceId": args.space_id or info["space_id"],
                   "currentDatetime": now_ms, "surface": "ai_module"}},
        {"id": user_step_id, "type": "user", "value": [["Reply with exactly: OK"]],
         "userId": info["user_id"], "createdAt": now_ms},
    ]
    body = {
        "spaceId": args.space_id or info["space_id"],
        "threadId": str(uuid.uuid4()),
        "createThread": True,
        "generateTitle": False,
        "traceId": str(uuid.uuid4()),
        "transcript": transcript,
        "threadType": "workflow",
        "asPatchResponse": True,
        "isPartialTranscript": False,
        "saveAllThreadOperations": True,
        "setUnreadState": True,
        "createdSource": "ai_module",
        "isUserInAnySalesAssistedSpace": False,
        "isSpaceSalesAssisted": False,
        "debugOverrides": {"annotationInferences": {}, "cachedInferences": {},
                           "emitAgentSearchExtractedResults": True, "emitInferences": False},
        "threadParentPointer": {"table": "space", "id": args.space_id or info["space_id"],
                                "spaceId": args.space_id or info["space_id"]},
    }
    t0 = time.time()
    try:
        r2 = s.post(f"{API}/runInferenceTranscript", json=body, headers=hdr, timeout=90)
        dt2 = round(time.time() - t0, 3)
        head = {k.lower(): v for k, v in r2.headers.items()}
        if not args.quiet:
            print(f"[inference] {r2.status_code} ({dt2}s) ct={head.get('content-type', '')[:50]}")
            print(f"[inference] body_head={r2.text[:800]!r}")
        record["inference"] = {"status": r2.status_code, "ms": int(dt2 * 1000),
                               "content_type": head.get("content-type", "")[:80],
                               "body_head": r2.text[:2000]}
    except Exception as e:
        dt2 = round(time.time() - t0, 3)
        if not args.quiet:
            print(f"[inference] EXC {type(e).__name__} {str(e)[:200]}")
        record["inference"] = {"status": 0, "ms": int(dt2 * 1000), "error": str(e)[:300]}

    LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(LOG_DIR / "ws_verify_runs.jsonl", "a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")
    print("LOG:", json.dumps(record, ensure_ascii=False)[:800])


if __name__ == "__main__":
    main()