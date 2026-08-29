import json, pathlib, sys, time
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from curl_cffi import requests as cffi

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
info = json.loads((DETAIL / "mt57jrhj0dn7@aitextextractor.com" / "probe.json").read_text(encoding="utf-8"))
acc = json.loads((DETAIL / "mt57jrhj0dn7@aitextextractor.com" / "account.json").read_text(encoding="utf-8"))
device_id = acc.get("device_id")

LOG = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\rotate_ready_probe.jsonl")

s = cffi.Session(impersonate="chrome", proxy="http://127.0.0.1:3067", timeout=120)
cookie = "; ".join(f'{c["name"]}={c["value"]}' for c in info["cookies"] if c.get("value"))
hdr = {
    "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    "Accept": "application/json, text/plain, */*", "Content-Type": "application/json",
    "origin": "https://app.notion.com", "referer": "https://app.notion.com/onboarding",
    "x-notion-active-user-header": info["user_id"],
    "x-notion-space-id": info["space_id"],
    "notion-client-version": info.get("client_version") or "23.13.20260720.1949",
    "cookie": cookie,
}

for round_no in range(1, 15):
    ts = time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime())
    r = s.post("https://app.notion.com/api/v3/createspace", json={
        "name": f"ready-probe-{round_no}",
        "planType": "personal", "planSelection": "personal",
        "initialPersona": "unfilled",
        "deviceId": device_id, "deviceType": "web-desktop",
        "source": "handle_root_redirect",
    }, headers=hdr, timeout=120)
    record = {"round": round_no, "ts": ts, "status": r.status_code, "body": r.text[:200]}
    with open(LOG, "a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")
    print(f"[{ts}] round{round_no}: {r.status_code} {r.text[:120]}")
    if r.status_code == 200:
        j = r.json()
        print("READY new_space:", j.get("spaceId"))
        break
    time.sleep(600)

print("PROBE_DONE")