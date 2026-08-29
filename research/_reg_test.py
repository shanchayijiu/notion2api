import json, sys, urllib.request, urllib.error, http.cookiejar, time
sys.stdout.reconfigure(encoding="utf-8", line_buffering=True)
cj = http.cookiejar.CookieJar()
op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))
def post(path, body):
    req = urllib.request.Request("http://127.0.0.1:8787"+path, data=json.dumps(body).encode("utf-8"), headers={"Content-Type":"application/json"})
    try:
        r = op.open(req, timeout=200); return r.status, json.loads(r.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode("utf-8"))
post("/admin/login", {"password":"n2a-admin-dev"})
t0=time.time()
s,b = post("/admin/accounts/register", {})
print("register: status=%s in %.1fs email=%s" % (s, time.time()-t0, b.get("email")))
if s == 200:
    email = b.get("email")
    r = op.open(urllib.request.Request("http://127.0.0.1:8787/admin/accounts", headers={"Content-Type":"application/json"}), timeout=30)
    accts = json.loads(r.read().decode("utf-8"))
    emails = [a.get("email") for a in (accts.get("accounts") or [])]
    print("pooled accounts:", emails)
    body = {"model":"gpt-5.4","account_email":email,"messages":[{"role":"user","content":"reply with OK only"}],"max_tokens":20}
    req = urllib.request.Request("http://127.0.0.1:8787/v1/chat/completions", data=json.dumps(body).encode("utf-8"), headers={"Authorization":"Bearer sk-notion2api-dev","Content-Type":"application/json"})
    try:
        rr = json.loads(urllib.request.urlopen(req, timeout=60).read().decode("utf-8"))
        print("new account chat OK:", repr(rr["choices"][0]["message"]["content"][:30]))
    except Exception as e:
        print("new account chat ERR:", e)