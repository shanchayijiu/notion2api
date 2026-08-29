import pathlib

p = pathlib.Path(r"C:\Users\Administrator\notion2api\internal\app\v4_p0_test.go")
t = p.read_text(encoding="utf-8")
old = '"messages": []map[string]any{{"role": "user", "content": "你好"}},'
new = '"messages": []map[string]any{{"role": "user", "content": "介绍一下你的功能"}},'
n = t.count(old)
t = t.replace(old, new)
p.write_text(t, encoding="utf-8")
print("replaced:", n)