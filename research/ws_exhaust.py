#!/usr/bin/env python3
"""ws_exhaust.py — 新空间额度验证 + quota 耗尽形态抓取（spike #1 最后一项）：
对指定空间连续发 runInferenceTranscript，观察：
- 新空间是否独立额度（刚建的空间直接能发）
- 耗尽时的精确返回形态（HTTP 状态 / NDJSON 内容 / record-map subType）

用法:
  python ws_exhaust.py --account <email> --space-id <空间> [--max 5]
                       [--proxy http://127.0.0.1:3067]
产出: research/logs/ws_exhaust_runs.jsonl
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


def build_body(info: dict, space_id: str) -> dict:
    now_ms = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"
    transcript = [
        {"id": str(uuid.uuid4()), "type": "config",
         "value": {"type": "workflow", "useWebSearch": False, "useSearchToolV2": False,
                   "useReadOnlyMode": True, "writerMode": False, "modelFromUser": False,
                   "enableAgentGenerateImage": False, "enableAgentDiffs": True,
                   "enableAgentAutomations": True, "enableAgentIntegrations": True,
                   "enableCustomAgents": True, "enableScriptAgent": True,
                   "enableMailExplicitToolCalls": True, "enableScriptAgentSlack": True,
                   "enableScriptAgentMail": True, "enableScriptAgentCalendar": True,
                   "enableCreateAndRunThread": True, "availableConnectors": [],
                   "customConnectorInfo": [], "searchScopes": []}},
        {"id": str(uuid.uuid4()), "type": "context",
         "value": {"timezone": "Asia/Shanghai", "userName": info.get("user_name", ""),
                   "userId": info["user_id"], "userEmail": info["email"],
                   "spaceName": "exhaust", "spaceId": space_id,
                   "currentDatetime": now_ms, "surface": "ai_module"}},
        {"id": str(uuid.uuid4()), "type": "user", "value": [["Say OK."]],
         "userId": info["user_id"], "createdAt": now_ms},
    ]
    return {
        "spaceId": space_id,
        "threadId": str(uuid.uuid4()),
        "createThread": True,
        "generateTitle": False,
        "traceId": str(uuid.uuid4()),
        "transcript": transcript,
        "threadType": "workflow",
        "asPatchResponse": True,
        "isPartialTranscript": False,
        "saveAllThreadOperations": False,
        "setUnreadState": True,
        "createdSource": "ai_module",
        "isUserInAnySalesAssistedSpace": False,
        "isSpaceSalesAssisted": False,
        "debugOverrides": {"annotationInferences": {}, "cachedInferences": {},
                           "emitAgentSearchExtractedResults": True, "emitInferences": False},
        "threadParentPointer": {"table": "space", "id": space_id, "spaceId": space_id},
    }


def scan_ndjson(text: str) -> dict:
    """扫 NDJSON 特征：record-map subType / error / quota 相关词。"""
    feat = {"types": set(), "subtypes": set(), "error_words": []}
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            j = json.loads(line)
        except Exception:
            continue
        feat["types"].add(str(j.get("type", "")))
        rm = j.get("recordMap", {})
        if isinstance(rm, dict):
            for t, m in rm.items():
                if not isinstance(m, dict):
                    continue
                for rid, rv in m.items():
                    v = rv.get("value", {})
                    if isinstance(v, dict):
                        st = v.get("subType") or v.get("subtype")
                        if st:
                            feat["subtypes"].add(str(st))
                        msg = v.get("message") or ""
                        if msg:
                            feat["error_words"].append(str(msg)[:120])
        txt = json.dumps(j, ensure_ascii=False)
        low = txt.lower()
        for w in ("quota", "exhaust", "limit", "not allowed", "entitlement"):
            if w in low:
                feat["error_words"].append(w)
    feat["types"] = sorted(feat["types"])
    feat["subtypes"] = sorted(feat["subtypes"])
    return feat


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--account", required=True)
    ap.add_argument("--space-id", required=True)
    ap.add_argument("--max", type=int, default=6)
    ap.add_argument("--gap", type=float, default=2.0)
    ap.add_argument("--proxy", default="http://127.0.0.1:3067")
    args = ap.parse_args()

    from curl_cffi import requests as cffi

    info = load_probe(args.account)
    s = cffi.Session(impersonate="chrome", proxy=args.proxy, timeout=120)
    hdr = base_headers(info, f"{APP_HOME}/", args.space_id)
    hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
    print(f"[{args.account}] space={args.space_id} max={args.max}")

    runs = []
    for i in range(1, args.max + 1):
        t0 = time.time()
        try:
            r = s.post(f"{API}/runInferenceTranscript", json=build_body(info, args.space_id),
                       headers=hdr, timeout=120)
            dt = round(time.time() - t0, 3)
        except Exception as e:
            dt = round(time.time() - t0, 3)
            print(f"  [#{i}] EXC {type(e).__name__} {str(e)[:150]}")
            runs.append({"i": i, "status": 0, "ms": int(dt * 1000), "exc": str(e)[:200]})
            break
        head = {k.lower(): v for k, v in r.headers.items()}
        feat = scan_ndjson(r.text)
        run = {"i": i, "status": r.status_code, "ms": int(dt * 1000),
               "ct": head.get("content-type", "")[:60],
               "features": feat, "body_head": r.text[:400]}
        runs.append(run)
        print(f"  [#{i}] status={r.status_code} ({dt}s) types={feat['types']} "
              f"subtypes={feat['subtypes']} words={feat['error_words'][:3]}")
        hdr["cookie"] += _new_cookies(s, info=info, prefix="; ")
        if r.status_code != 200 or "quota-exhausted" in feat["subtypes"]:
            print(f"  STOP: status={r.status_code} (quota-exhausted={('quota-exhausted' in feat['subtypes'])})")
            break
        time.sleep(args.gap)

    LOG_DIR.mkdir(parents=True, exist_ok=True)
    with open(LOG_DIR / "ws_exhaust_runs.jsonl", "a", encoding="utf-8") as f:
        f.write(json.dumps({"ts": datetime.now(timezone.utc).isoformat(),
                            "account": args.account, "space_id": args.space_id,
                            "runs": runs}, ensure_ascii=False) + "\n")
    last = runs[-1] if runs else {}
    print(f"LAST: status={last.get('status')} features={last.get('features')}")


if __name__ == "__main__":
    main()