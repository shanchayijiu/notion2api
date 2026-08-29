import json, pathlib, sys, uuid, time
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from curl_cffi import requests as cffi

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
EMAIL = "mt6rrdq5mb9r@imageeditgpt.com"
info = json.loads((DETAIL / EMAIL / "probe.json").read_text(encoding="utf-8"))
nb_id = next(c["value"] for c in info["cookies"] if c["name"] == "notion_browser_id")
LOG = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\crud_cycle.jsonl")

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

def create(tag):
    body = {
        "name": f"{tag}-{uuid.uuid4().hex[:6]}",
        "icon": "🏠",
        "planType": "personal", "planSelection": "personal",
        "initialPersona": "unfilled",
        "deviceId": nb_id, "deviceType": "web-desktop",
        "source": "handle_root_redirect",
        "createSpaceView": True,
    }
    r = s.post(f"{API}/createspace", json=body, headers=hdr, timeout=120)
    sid = ""
    if r.status_code == 200:
        sid = r.json().get("spaceId", "")
    return r.status_code, sid, r.text[:150]

def delete(sid):
    r = s.post(f"{API}/deleteSpace", json={"spaceId": sid}, headers=hdr, timeout=60)
    return r.status_code, r.text[:150]

print("CRUD cycle on", EMAIL)
for cycle in range(1, 5):
    ts = time.strftime("%H:%M:%S", time.localtime())
    st, sid, tb = create(f"crud{cycle}")
    print(f"[{ts}] C{cycle}: {st} {sid[:16] if sid else tb}")
    rec = {"ts": ts, "cycle": cycle, "op": "create", "status": st, "space_id": sid, "body": tb}
    if st == 200:
        time.sleep(8)
        dst, dtb = delete(sid)
        print(f"        D{cycle}: {dst} {dtb[:100]}")
        rec = {"ts": ts, "cycle": cycle, "op": "create+delete", "create": st, "delete": dst, "space_id": sid}
        LOG.parent.mkdir(parents=True, exist_ok=True)
        with open(LOG, "a", encoding="utf-8") as f:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")
        if cycle < 4:
            print("  (waiting 3min)")
            time.sleep(180)
    else:
        print("  STOP: create failed")
        break
print("CRUD_DONE")