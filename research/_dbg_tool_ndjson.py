import json, pathlib, sys
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from curl_cffi import requests as cffi

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
EMAIL = "mt6rrdq5mb9r@imageeditgpt.com"
info = json.loads((DETAIL / EMAIL / "probe.json").read_text(encoding="utf-8"))
s = cffi.Session(impersonate="chrome142", proxy="http://127.0.0.1:7890", timeout=120)
cookie = "; ".join(f'{c["name"]}={c["value"]}' for c in info["cookies"] if c.get("value"))
hdr = {
    "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
    "Accept": "application/json, text/plain, */*", "Content-Type": "application/json",
    "origin": "https://app.notion.com", "referer": "https://app.notion.com/",
    "x-notion-active-user-header": info["user_id"],
    "x-notion-space-id": info["space_id"],
    "notion-client-version": info.get("client_version") or "23.13.20260720.1949",
    "cookie": cookie,
}
now_ms = "2026-08-24T19:00:00.000Z"
transcript = [
    {"id": "cfg", "type": "config", "value": {"type": "workflow", "useWebSearch": False, "useReadOnlyMode": False, "modelFromUser": False, "availableConnectors": [], "customConnectorInfo": [], "searchScopes": []}},
    {"id": "ctx", "type": "context", "value": {"timezone": "Asia/Shanghai", "userName": "", "userId": info["user_id"], "userEmail": info["email"], "spaceName": "t", "spaceId": info["space_id"], "currentDatetime": now_ms, "surface": "ai_module"}},
    {"id": "usr", "type": "user", "value": [["Use the filesystem tools to read modules/fs/index.ts and tell me its first line."]], "userId": info["user_id"], "createdAt": now_ms},
]
body = {"spaceId": info["space_id"], "threadId": "t3", "createThread": True, "generateTitle": False,
        "traceId": "tr3", "transcript": transcript, "threadType": "workflow", "asPatchResponse": True,
        "isPartialTranscript": False, "saveAllThreadOperations": False, "setUnreadState": True,
        "createdSource": "ai_module", "isUserInAnySalesAssistedSpace": False, "isSpaceSalesAssisted": False,
        "debugOverrides": {"annotationInferences": {}, "cachedInferences": {}, "emitAgentSearchExtractedResults": True, "emitInferences": False},
        "threadParentPointer": {"table": "space", "id": info["space_id"], "spaceId": info["space_id"]}}
r = s.post("https://app.notion.com/api/v3/runInferenceTranscript", json=body, headers=hdr, timeout=120)
print("status:", r.status_code)
out = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\raw_tool_ndjson.jsonl")
out.write_text(r.text, encoding="utf-8")
lines = [ln for ln in r.text.splitlines() if ln.strip()]
print("lines:", len(lines))
for i, ln in enumerate(lines):
    if "tool" in ln.lower():
        j = json.loads(ln)
        for op in j.get("v", []):
            nv = op.get("v")
            if isinstance(nv, dict) and "tool" in str(nv.get("type", "")).lower():
                print(f"--- line {i} p={op.get('p','')}")
                print("   ", json.dumps(nv, ensure_ascii=False)[:500])