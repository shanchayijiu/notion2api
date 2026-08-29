import json, pathlib, sys, re
sys.stdout.reconfigure(encoding="utf-8", errors="replace")
body = pathlib.Path(r"C:\Users\Administrator\notion2api\research\logs\raw_tool2.jsonl").read_text(encoding="utf-8", errors="replace")
seen = set()
for m in re.finditer(r'"notionModelName":"([^"]+)"', body):
    seen.add(m.group(1))
print("browser notionModelNames:", seen)

g = json.loads(pathlib.Path(r"C:\Users\Administrator\notion2api\tmp_last_runInferenceTranscript_body.json").read_text(encoding="utf-8", errors="ignore"))
print("service payload keys:", list(g.keys()))
print("service debugOverrides:", json.dumps(g.get("debugOverrides"), ensure_ascii=False))
print("service threadType:", g.get("threadType"), "createThread:", g.get("createThread"))
# 浏览器请求的 config 从 raw_tool2 前的浏览器请求拿不到；对比服务 config 与 notion_manager 的 applyWorkflowRequestProtocol
# 看 service config 的 agent 相关键
for s in g.get("transcript", []):
    if s.get("type") == "config":
        c = s.get("value", {})
        for k in ["enableAgentThreadTools", "enableCrdtOperations", "enableAgentDiffs", "enableAgentUpdatePagePatch"]:
            print("  config", k, "=", c.get(k))