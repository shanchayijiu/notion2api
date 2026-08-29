import pathlib

p = pathlib.Path(r"C:\Users\Administrator\notion2api\internal\app\main.go")
t = p.read_text(encoding="utf-8")
old = "hasTools := len(sliceValue(typed.Tools)) > 0"
new = 'hasTools := len(sliceValue(typed.Tools)) > 0 && !toolChoiceNone(payload["tool_choice"])'
n = t.count(old)
t = t.replace(old, new)
p.write_text(t, encoding="utf-8")
print("replaced:", n)