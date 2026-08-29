import pathlib

p = pathlib.Path(r"C:\Users\Administrator\notion2api\internal\app\main.go")
t = p.read_text(encoding="utf-8")
old = 'toolCalls = synthesizeToolCall(rawResultText, parseToolList(typed.Tools), inputAny, false, "")'
new = "toolCalls = synthesizeToolCall(rawResultText, parseToolList(typed.Tools), inputAny, responsesToolChoiceForced, responsesForcedName)"
n = t.count(old)
t = t.replace(old, new)
p.write_text(t, encoding="utf-8")
print("replaced:", n)