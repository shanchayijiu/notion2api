import pathlib
p = pathlib.Path(r"C:\Users\Administrator\notion2api\internal\app\notion_client.go")
t = p.read_text(encoding="utf-8")
old = '"enableAgentThreadTools":                         false,'
new = '"enableAgentThreadTools":                         true,'
if old in t:
    p.write_text(t.replace(old, new, 1), encoding="utf-8")
    print("changed to true")
else:
    i = t.find("enableAgentThreadTools")
    print("not found, ctx:", repr(t[max(0, i - 60):i + 60]))