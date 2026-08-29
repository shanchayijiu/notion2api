import json, pathlib, sys, uuid, time
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from curl_cffi import requests as cffi

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
EMAIL = "mt6rrdq5mb9r@imageeditgpt.com"
info = json.loads((DETAIL / EMAIL / "probe.json").read_text(encoding="utf-8"))
nb_id = next(c["value"] for c in info["cookies"] if c["name"] == "notion_browser_id")
LOG = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\rotate_e2e.jsonl")

s = cffi.Session(impersonate="chrome142", proxy="http://127.0.0.1:7890", timeout=60)
cookie = "; ".join(f'{c["name"]}={c["value"]}' for c in info["cookies"] if c.get("value"))
hdr = {
    "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
    "Accept": "application/json, text/plain, */*", "Content-Type": "application/json",
    "origin": "https://app.notion.com", "referer": "https://app.notion.com/",
    "x-notion-active-user-header": info["user_id"],
    "x-notion-space-id": info["space_id"],
    "notion-client-version": info.get("client_version") or "23.13.20260720.1949",
    "cookie": cookie,
    "sec-ch-ua": '"Chromium";v="142", "Not?A_Brand";v="8", "Google Chrome";v="142"',
    "sec-ch-ua-mobile": "?0", "sec-ch-ua-platform": '"Windows"',
    "sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
}
API = "https://app.notion.com/api/v3"

def create():
    body = {
        "name": f"rot-{uuid.uuid4().hex[:6]}",
        "icon": "🏠",
        "planType": "personal", "planSelection": "personal",
        "initialPersona": "unfilled",
        "deviceId": nb_id, "deviceType": "web-desktop",
        "source": "handle_root_redirect",
        "createSpaceView": True,
    }
    for attempt in range(1, 4):
        r = s.post(f"{API}/createspace", json=body, headers=hdr, timeout=120)
        if r.status_code == 200:
            return r.status_code, r.json().get("spaceId", "")
        if r.status_code == 504 and attempt < 3:
            print(f"    504 retry {attempt}...")
            time.sleep(20)
            continue
        return r.status_code, ""
    return 0, ""

def inference(space_id):
    now_ms = datetime_iso()
    hdr2 = dict(hdr)
    hdr2["x-notion-space-id"] = space_id
    transcript = [
        {"id": str(uuid.uuid4()), "type": "config",
         "value": {"type": "workflow", "useWebSearch": False, "useReadOnlyMode": False,
                   "modelFromUser": False, "enableAgentGenerateImage": False,
                   "availableConnectors": [], "customConnectorInfo": [], "searchScopes": []}},
        {"id": str(uuid.uuid4()), "type": "context",
         "value": {"timezone": "Asia/Shanghai", "userName": "", "userId": info["user_id"],
                   "userEmail": info["email"], "spaceName": "rot", "spaceId": space_id,
                   "currentDatetime": now_ms, "surface": "ai_module"}},
        {"id": str(uuid.uuid4()), "type": "user",
         "value": [["Reply with exactly: OK"]],
         "userId": info["user_id"], "createdAt": now_ms},
    ]
    body = {"spaceId": space_id, "threadId": str(uuid.uuid4()),
            "createThread": True, "generateTitle": False, "traceId": str(uuid.uuid4()),
            "transcript": transcript, "threadType": "workflow", "asPatchResponse": True,
            "isPartialTranscript": False, "saveAllThreadOperations": False,
            "setUnreadState": True, "createdSource": "ai_module",
            "isUserInAnySalesAssistedSpace": False, "isSpaceSalesAssisted": False,
            "debugOverrides": {"annotationInferences": {}, "cachedInferences": {},
                               "emitAgentSearchExtractedResults": True, "emitInferences": False},
            "threadParentPointer": {"table": "space", "id": space_id, "spaceId": space_id}}
    r = s.post(f"{API}/runInferenceTranscript", json=body, headers=hdr2, timeout=120)
    return r.status_code, r.text[:100]

def datetime_iso():
    from datetime import datetime, timezone
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"

print("3-space rotation E2E on", EMAIL)
spaces = []
for i in range(1, 4):
    ts = time.strftime("%H:%M:%S", time.localtime())
    st, sid = create()
    print(f"[{ts}] create#{i}: {st} {sid[:16]}")
    if st != 200:
        print("  STOP")
        break
    spaces.append(sid)
    time.sleep(5)
    ist, ib = inference(sid)
    print(f"        inference: {ist} {ib[:80]}")
    rec = {"ts": ts, "i": i, "create": st, "space_id": sid, "inference": ist}
    LOG.parent.mkdir(parents=True, exist_ok=True)
    with open(LOG, "a", encoding="utf-8") as f:
        f.write(json.dumps(rec, ensure_ascii=False) + "\n")
    if i < 3:
        print("  (3min gap)")
        time.sleep(180)
print("SPACES:", json.dumps(spaces, ensure_ascii=False))
print("ROTATE_E2E_DONE")