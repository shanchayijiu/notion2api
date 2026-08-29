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
r = s.post("https://app.notion.com/api/v3/getAvailableModels",
           json={"spaceId": info["space_id"]}, headers=hdr, timeout=60)
print("getAvailableModels:", r.status_code)
j = r.json()
print("top keys:", list(j.keys())[:8])
models = j.get("models", [])
print("models count:", len(models))
if models:
    print("sample keys:", list(models[0].keys()))
    print("sample:", json.dumps(models[0], ensure_ascii=False)[:400])
    # 落盘为 probe 格式参考
    out = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\models_blob_sample.json")
    out.write_text(json.dumps({"models": models}, ensure_ascii=False, indent=1), encoding="utf-8")
    print("saved", out)