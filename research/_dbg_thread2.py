import json, pathlib, sys
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from curl_cffi import requests as cffi

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
EMAIL = "mt6rrdq5mb9r@imageeditgpt.com"
info = json.loads((DETAIL / EMAIL / "probe.json").read_text(encoding="utf-8"))
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
}
API = "https://app.notion.com/api/v3"
THREAD = "3c68c1ed-b9f1-803b-bd5f-00a98b40e68b"
SID = "f9d8c1ed-b9f1-8155-a056-0003764c935d"

# 只查 thread（不带 spaceId）
body = {"requests": [{"pointer": {"table": "thread", "id": THREAD}, "version": -1}]}
r = s.post(f"{API}/syncRecordValuesMain", json=body, headers=hdr, timeout=60)
print("thread-only:", r.status_code, r.text[:300])
if r.status_code == 200:
    rm = r.json().get("recordMap", {})
    th = rm.get("thread", {})
    for rid, rv in th.items():
        v = rv.get("value", {})
        print("  thread keys:", list(v.keys()))
        print("  messages:", v.get("messages"))
        print("  FULL:", json.dumps(v, ensure_ascii=False)[:800])