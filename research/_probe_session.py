import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

probe = json.load(open(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail\mt57jrhj0dn7@aitextextractor.com\probe.json", encoding="utf-8"))
cookies = "; ".join(f"{c['name']}={c['value']}" for c in probe.get("cookies", []))
print("cookie keys:", [c["name"] for c in probe.get("cookies", [])])

req = urllib.request.Request(
    "https://www.notion.so/api/v3/syncRecordValuesSpaceInitial",
    data=json.dumps({"spaceId": probe["space_id"]}).encode(),
    headers={"Content-Type": "application/json", "Cookie": cookies, "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"},
)
proxy_handler = urllib.request.ProxyHandler({"http": "http://127.0.0.1:3067", "https": "http://127.0.0.1:3067"})
opener = urllib.request.build_opener(proxy_handler)
try:
    r = opener.open(req, timeout=30)
    data = r.read()[:200]
    print("syncRecordValuesSpaceInitial:", r.status, data[:120])
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read()[:150])
except Exception as e:
    print("ERR:", e)