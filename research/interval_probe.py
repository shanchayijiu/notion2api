import json, pathlib, sys, time, uuid
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from curl_cffi import requests as cffi

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
EMAIL = "mt6puecq4ubt@imageeditgpt.com"
info = json.loads((DETAIL / EMAIL / "probe.json").read_text(encoding="utf-8"))
nb_id = next(c["value"] for c in info["cookies"] if c["name"] == "notion_browser_id")
LOG = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\interval_probe.jsonl")

s = cffi.Session(impersonate="chrome142", proxy="http://127.0.0.1:7890", timeout=60)
cookie = "; ".join(f'{c["name"]}={c["value"]}' for c in info["cookies"] if c.get("value"))
hdr = {
    "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
    "Accept": "application/json, text/plain, */*", "Content-Type": "application/json",
    "origin": "https://app.notion.com", "referer": "https://app.notion.com/onboarding",
    "x-notion-active-user-header": info["user_id"],
    "x-notion-space-id": info["space_id"],
    "notion-client-version": info.get("client_version") or "23.13.20260720.1949",
    "cookie": cookie,
    "sec-ch-ua": '"Chromium";v="142", "Not?A_Brand";v="8", "Google Chrome";v="142"',
    "sec-ch-ua-mobile": "?0", "sec-ch-ua-platform": '"Windows"',
    "sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
}
API = "https://app.notion.com/api/v3"

for round_no in range(1, 7):
    ts = time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime())
    body = {
        "name": f"iv-{uuid.uuid4().hex[:6]}",
        "icon": "🏠",
        "planType": "personal", "planSelection": "personal",
        "initialPersona": "unfilled",
        "deviceId": nb_id, "deviceType": "web-desktop",
        "source": "handle_root_redirect",
        "createSpaceView": True,
    }
    r = s.post(f"{API}/createspace", json=body, headers=hdr, timeout=120)
    rec = {"round": round_no, "ts": ts, "status": r.status_code, "body": r.text[:200]}
    with open(LOG, "a", encoding="utf-8") as f:
        f.write(json.dumps(rec, ensure_ascii=False) + "\n")
    print(f"[{ts}] round{round_no}: {r.status_code} {r.text[:120]}")
    if r.status_code == 200:
        print("OK spaceId:", r.json().get("spaceId"))
    time.sleep(600)  # 10 分钟间隔

print("PROBE_DONE")