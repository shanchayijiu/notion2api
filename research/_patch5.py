import pathlib

p = pathlib.Path(r"C:\Users\Administrator\notion2api\internal\app\main.go")
t = p.read_text(encoding="utf-8")
old = 'safeWriter := &panicSafeResponseWriter{ResponseWriter: w}'
new = 'log.Printf("[inbound] %s %s", r.Method, r.URL.Path)\n\tsafeWriter := &panicSafeResponseWriter{ResponseWriter: w}'
n = t.count(old)
t = t.replace(old, new, 1)
p.write_text(t, encoding="utf-8")
print("replaced:", n)