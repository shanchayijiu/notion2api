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
# 服务刚跑的 fs2 请求体（GALIAIS 格式完整）
body = json.loads(pathlib.Path(r"C:\Users\Administrator\notion2api\tmp_last_runInferenceTranscript_body.json").read_text(encoding="utf-8", errors="ignore"))
r = s.post("https://app.notion.com/api/v3/runInferenceTranscript", json=body, headers=hdr, timeout=120)
print("status:", r.status_code)
out = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\galiais_replay.jsonl")
out.write_text(r.text, encoding="utf-8")
print("saved len:", len(r.text))
for i, ln in enumerate(r.text.splitlines()):
    if not ln.strip():
        continue
    if "tool" in ln.lower():
        try:
            j = json.loads(ln)
        except Exception:
            continue
        for op in j.get("v", []):
            nv = op.get("v")
            if isinstance(nv, dict) and "tool" in str(nv.get("type", "")).lower():
                print(f"line {i}: type={nv.get('type')} p={op.get('p','')} input={json.dumps(nv.get('input'), ensure_ascii=False)[:150]}")