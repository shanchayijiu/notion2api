import json, sys, urllib.request, urllib.error, time

sys.stdout.reconfigure(encoding="utf-8")

probe = json.load(open(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail\mt57jrhj0dn7@aitextextractor.com\probe.json", encoding="utf-8"))
cookies = "; ".join(f"{c['name']}={c['value']}" for c in probe.get("cookies", []))

proxy_handler = urllib.request.ProxyHandler({"http": "http://127.0.0.1:3067", "https": "http://127.0.0.1:3067"})
opener = urllib.request.build_opener(proxy_handler)

body = json.dumps({"spaceId": probe["space_id"]}).encode()
headers = {"Content-Type": "application/json", "Cookie": cookies, "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"}

# 同一 opener（连接复用）连续 3 次
for i in range(3):
    req = urllib.request.Request(
        "https://www.notion.so/api/v3/syncRecordValuesSpaceInitial",
        data=body, headers=headers,
    )
    try:
        t0 = time.time()
        r = opener.open(req, timeout=20)
        dt = time.time() - t0
        print(f"req{i+1}: {r.status} in {dt:.1f}s")
    except urllib.error.HTTPError as e:
        print(f"req{i+1}: HTTP {e.code} in {time.time()-t0:.1f}s")
    except Exception as e:
        print(f"req{i+1}: ERR {e} in {time.time()-t0:.1f}s")