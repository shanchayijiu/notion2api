import http.client, json, sys, time, threading
sys.stdout.reconfigure(encoding='utf-8')

PROBE = json.load(open(r'C:\Users\Administrator\Desktop\notion注册机\register\accounts\detail\mt57jrhj0dn7@aitextextractor.com\probe.json', encoding='utf-8'))
SYNC = json.dumps({'requests': [{'pointer': {'table': 'thread', 'id': '12202ed6-a804-471c-b806-d800783824e5', 'spaceId': PROBE['space_id']}, 'version': -1}]})

def cookie_header():
    return '; '.join(f"{c['name']}={c['value']}" for c in PROBE.get('cookies', []))

def headers():
    return {
        'Content-Type': 'application/json',
        'Accept': 'application/json',
        'Cookie': cookie_header(),
        'X-Notion-Active-User-Header': PROBE.get('user_id', ''),
        'X-Notion-Space-Id': PROBE.get('space_id', ''),
        'Notion-Client-Version': PROBE.get('client_version', '23.13.20260720.1949'),
        'Origin': 'https://app.notion.com',
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36',
    }

def worker(idx):
    t0 = time.time()
    try:
        conn = http.client.HTTPSConnection('127.0.0.1', 3067, timeout=25)
        conn.set_tunnel('www.notion.so', 443)
        conn.connect()
        conn.request('POST', '/api/v3/syncRecordValuesSpaceInitial', body=SYNC.encode('utf-8'), headers=headers())
        r = conn.getresponse()
        body = r.read(80)
        print(f'conn{idx}: status={r.status} in {time.time()-t0:.1f}s resp={body[:60]!r}')
        conn.close()
    except Exception as e:
        print(f'conn{idx}: ERR {time.time()-t0:.1f}s {e}')

threads = [threading.Thread(target=worker, args=(i,)) for i in range(3)]
for t in threads: t.start()
for t in threads: t.join()
print('all done')