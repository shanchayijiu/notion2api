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

# 查 thread 完整记录（data 字段含 turn_outcomes?）
body = {"requests": [{"pointer": {"table": "thread", "id": THREAD}, "version": -1}]}
r = s.post(f"{API}/syncRecordValuesMain", json=body, headers=hdr, timeout=60)
j = r.json()
th = j["recordMap"]["thread"][THREAD]["value"]["value"]
print("thread data keys:", list(th.get("data", {}).keys()) if isinstance(th.get("data"), dict) else type(th.get("data")))
data = th.get("data", {})
if isinstance(data, dict):
    for k, v in data.items():
        print(f"  data.{k}:", json.dumps(v, ensure_ascii=False)[:400])
# 试试直接查 message 用 block 表
msg_ids = th["messages"]
body2 = {"requests": [{"pointer": {"table": "block", "id": m}, "version": -1} for m in msg_ids[:5]]}
r2 = s.post(f"{API}/syncRecordValuesMain", json=body2, headers=hdr, timeout=60)
print("block query:", r2.status_code, r2.text[:200])