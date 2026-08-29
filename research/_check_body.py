import json, sys

sys.stdout.reconfigure(encoding="utf-8")
b = json.load(open(r"C:\Users\Administrator\notion2api\tmp_last_runInferenceTranscript_body.json", encoding="utf-8"))
t = b.get("transcript", [])
print("steps:", [(m.get("type"), len(json.dumps(m.get("value", "")))) for m in t])
for m in t:
    if m.get("type") in ("user", "assistant"):
        s = m.get("value", [[]])[0][0]
        print(f"--- {m['type']} ---")
        print(s[:250])