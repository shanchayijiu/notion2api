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

# 先拿 thread 的 messages 列表
THREAD = "3c68c1ed-b9f1-803b-bd5f-00a98b40e68b"
body = {"requests": [{"pointer": {"table": "thread", "id": THREAD}, "version": -1}]}
r = s.post(f"{API}/syncRecordValuesMain", json=body, headers=hdr, timeout=60)
th = r.json()["recordMap"]["thread"][THREAD]["value"]["value"]
msgs = th["messages"]
print("messages:", len(msgs))

# 查 message 记录
body2 = {"requests": [{"pointer": {"table": "message", "id": m}, "version": -1} for m in msgs]}
r2 = s.post(f"{API}/syncRecordValuesMain", json=body2, headers=hdr, timeout=60)
print("query messages:", r2.status_code)
if r2.status_code == 200:
    rm = r2.json().get("recordMap", {})
    print("tables:", list(rm.keys()))
    mtab = rm.get("message", {})
    print("messages found:", len(mtab))
    for rid, rv in list(mtab.items()):
        v = rv.get("value", {})
        if not isinstance(v, dict):
            print(f"  [{rid}] non-dict:", rv)
            continue
        keys = list(v.keys())
        text = json.dumps(v, ensure_ascii=False)
        print(f"\n== msg {rid[:16]} keys={keys[:12]}")
        for kw in ["thought", "thinking", "encryptedContent", "Assuming", "head start", "text"]:
            if kw.lower() in text.lower():
                i = text.lower().find(kw.lower())
                print(f"   [{kw}] @ {i}:", text[max(0, i-100):i+250][:350])
        print("   head:", text[:400])