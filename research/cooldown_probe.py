import json, pathlib, sys, time, subprocess
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
from curl_cffi import requests as cffi

DETAIL = pathlib.Path(r"C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail")
info = json.loads((DETAIL / "mt4bm39s21os@aitextextractor.com" / "probe.json").read_text(encoding="utf-8"))
nb_id = next(c["value"] for c in info["cookies"] if c["name"] == "notion_browser_id")
domain = info["email"].split("@")[1]
LOG = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\cooldown_probe.jsonl")

UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
s = cffi.Session(impersonate="chrome142", proxy="http://127.0.0.1:7890", timeout=60)
cookie = "; ".join(f'{c["name"]}={c["value"]}' for c in info["cookies"] if c.get("value"))
hdr = {
    "User-Agent": UA,
    "Accept": "application/json, text/plain, */*", "Content-Type": "application/json",
    "origin": "https://app.notion.com", "referer": "https://app.notion.com/onboarding",
    "x-notion-active-user-header": info["user_id"],
    "x-notion-space-id": info["space_id"],
    "notion-client-version": info.get("client_version") or "23.13.20260720.1949",
    "cookie": cookie,
    "sec-ch-ua": '"Chromium";v="142", "Not?A_Brand";v="8", "Google Chrome";v="142"',
    "sec-ch-ua-mobile": "?0", "sec-ch-ua-platform": '"Windows"',
    "sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
}
API = "https://app.notion.com/api/v3"

body = {
    "name": "cooldown-probe",
    "domain": domain, "emailDomains": [domain], "icon": None,
    "planType": "personal", "planSelection": "personal", "initialPersona": "unfilled",
    "shouldCreateUserPersonaTeam": False, "shouldMakeUserPersonaTeamDefault": False,
    "domainType": "personal", "collaborativeIntent": None,
    "deviceId": nb_id, "deviceType": "web-desktop", "desktopTargetPlatform": "web",
    "source": "handle_root_redirect", "createSpaceView": True,
}

for round_no in range(1, 13):
    ts = time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime())
    r = s.post(f"{API}/createspace", json=body, headers=hdr, timeout=120)
    rec = {"round": round_no, "ts": ts, "status": r.status_code, "body": r.text[:200]}
    with open(LOG, "a", encoding="utf-8") as f:
        f.write(json.dumps(rec, ensure_ascii=False) + "\n")
    print(f"[{ts}] round{round_no}: {r.status_code} {r.text[:140]}")
    if r.status_code == 200:
        j = r.json()
        print("READY spaceId:", j.get("spaceId"))
        print("spaceViewPointer:", json.dumps(j.get("spaceViewPointer"), ensure_ascii=False)[:200])
        # 200 → 跑 Go 完整轮换验证
        print("running Go TestRotateProbe...")
        env = dict(__import__("os").environ)
        env["ROTATE_PROBE"] = "1"
        res = subprocess.run(
            [r"C:\Program Files\Go\bin\go.exe", "test", "./internal/app", "-run", "TestRotateProbe", "-v", "-timeout", "300s"],
            cwd=r"C:\Users\Administrator\notion2api", env=env, capture_output=True, text=True, timeout=400)
        print(res.stdout[-1500:])
        print(res.stderr[-500:])
        break
    time.sleep(1200)  # 20 分钟

print("PROBE_DONE")