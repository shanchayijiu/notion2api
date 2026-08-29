import http.client, json, sys, time, re
sys.stdout.reconfigure(encoding='utf-8')

HOST = 'www.notion.so'
PROBE = json.load(open(r'C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail\mt57jrhj0dn7@aitextextractor.com\probe.json', encoding='utf-8'))
BODY = json.load(open(r'C:\Users\Administrator\notion2api\tmp_last_runInferenceTranscript_body.json', encoding='utf-8'))

def cookie_header():
    parts = []
    for c in PROBE.get('cookies', []):
        parts.append(f"{c['name']}={c['value']}")
    return '; '.join(parts)

def headers(ctype='application/json'):
    return {
        'Content-Type': ctype,
        'Accept': 'application/x-ndjson' if ctype == 'application/json' else 'application/json',
        'Cookie': cookie_header(),
        'X-Notion-Active-User-Header': PROBE.get('user_id', ''),
        'X-Notion-Space-Id': PROBE.get('space_id', ''),
        'Notion-Client-Version': PROBE.get('client_version', '23.13.20260720.1949'),
        'Origin': 'https://app.notion.com',
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36',
    }

conn = http.client.HTTPSConnection('127.0.0.1', 3067, timeout=60)
conn.set_tunnel('www.notion.so', 443)
conn.connect()

# 1) runInferenceTranscript (NDJSON stream) - read only 3 lines then stop
t0 = time.time()
payload = json.dumps(BODY, ensure_ascii=False).encode('utf-8')
conn.request('POST', '/api/v3/runInferenceTranscript', body=payload, headers=headers())
r = conn.getresponse()
lines = []
buf = b''
deadline = time.time() + 30
while time.time() < deadline and len(lines) < 3:
    chunk = r.read1(4096) if hasattr(r, 'read1') else r.read(4096)
    if not chunk:
        break
    buf += chunk
    while b'\n' in buf:
        line, buf = buf.split(b'\n', 1)
        lines.append(line)
        if len(lines) >= 3:
            break
print(f'ndjson: status={r.status} lines={len(lines)} in {time.time()-t0:.1f}s')
r.close()  # do NOT fully drain - simulate service behavior

# 2) same connection: syncRecordValuesSpaceInitial
t0 = time.time()
sync_payload = json.dumps({'requests': [{'pointer': {'table': 'thread', 'id': BODY.get('threadId') or '', 'spaceId': PROBE.get('space_id', '')}, 'version': -1}]})
conn.request('POST', '/api/v3/syncRecordValuesSpaceInitial', body=sync_payload.encode('utf-8'), headers=headers('application/json'))
try:
    r2 = conn.getresponse()
    body2 = r2.read(200)
    print(f'sync: status={r2.status} in {time.time()-t0:.1f}s resp={body2[:80]!r}')
except Exception as e:
    print(f'sync: ERR {time.time()-t0:.1f}s {e}')
conn.close()