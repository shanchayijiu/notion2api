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

# 查 train 对话 thread（含 Thought 块的那个）的消息记录
THREAD = "3c68c1ed-b9f1-803b-bd5f-00a98b40e68b"  # Train meeting time problem
MESSAGES = ["3c68c1ed-b9f1-805d-b345-00aaae0ba7cc", "3c68c1ed-b9f1-8022-9715-00aa6f65bb96",
            "3c68c1ed-b9f1-8089-bf86-00aa022d24fe"]

body = {"requests": [
    {"pointer": {"table": "thread", "id": THREAD, "spaceId": "f9d8c1ed-b9f1-8155-a056-0003764c935d"}, "version": -1}] + [
    {"pointer": {"table": "message", "id": m, "spaceId": "f9d8c1ed-b9f1-8155-a056-0003764c935d"}, "version": -1} for m in MESSAGES]}
r = s.post(f"{API}/syncRecordValuesMain", json=body, headers=hdr, timeout=60)
print("status:", r.status_code)
j = r.json()
rm = j.get("recordMap", {})
print("tables:", list(rm.keys()))
for t in ["thread", "message"]:
    m = rm.get(t)
    if not isinstance(m, dict):
        continue
    for rid, rv in m.items():
        v = rv.get("value", {})
        if isinstance(v, dict):
            print(f"\n== [{t}] {rid}")
            print("  keys:", list(v.keys()))
            # 找 thinking/encryptedContent/明文
            text = json.dumps(v, ensure_ascii=False)
            for kw in ["thought", "thinking", "encryptedContent", "Assuming", "head start"]:
                if kw.lower() in text.lower():
                    i = text.lower().find(kw.lower())
                    print(f"  [{kw}] @ {i}:", text[max(0, i-200):i+300][:500])
            # message 的完整 content（可能含明文 thinking）
            if t == "message":
                print("  FULL:", text[:1500])