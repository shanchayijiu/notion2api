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

# thread -> messages
body = {"requests": [{"pointer": {"table": "thread", "id": THREAD}, "version": -1}]}
r = s.post(f"{API}/syncRecordValuesMain", json=body, headers=hdr, timeout=60)
msgs = r.json()["recordMap"]["thread"][THREAD]["value"]["value"]["messages"]
print("messages:", len(msgs))

# block 查询（完整 value）
body2 = {"requests": [{"pointer": {"table": "block", "id": m}, "version": -1} for m in msgs]}
r2 = s.post(f"{API}/syncRecordValuesMain", json=body2, headers=hdr, timeout=60)
print("block query:", r2.status_code)
j2 = r2.json()
bm = j2.get("recordMap", {}).get("block", {})
print("blocks:", len(bm))
for rid, rv in bm.items():
    v = rv.get("value", {})
    if not isinstance(v, dict):
        print(f"[{rid[:16]}] type={type(v).__name__}")
        continue
    text = json.dumps(v, ensure_ascii=False)
    print(f"\n== block {rid[:16]} keys={list(v.keys())[:10]}")
    for kw in ["Assuming", "head start", "Thought", "thought", "content"]:
        if kw.lower() in text.lower():
            i = text.lower().find(kw.lower())
            print(f"  [{kw}] @ {i}:", text[max(0, i-150):i+300][:450])
    print("  head:", text[:500])