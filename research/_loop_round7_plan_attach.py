import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

csv_path = r'C:\Users\Administrator\notion2api\research\loop_intel_bundle.csv'
print('csv size:', __import__('os').path.getsize(csv_path))

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": (
                "你是本项目架构顾问（循环工作流第 7 轮）。\n"
                "请读取附件 loop_intel_bundle.csv（真实情报包：框架图+当前代码片段+账号状态+已验证事实，分 3-4 段存在 part1..partN 单元格，请完整读完全部 part 再回答）。"
                "基于附件内容（不要凭空猜）回答：\n"
                "A. 三大痛点根因排序（回复极慢/无流式/工具调用烂；最可能→最不可能，给机理，引用附件里的函数名）\n"
                "B. 修复方案（最小改动、具体到函数/段、可验证）\n"
                "C. 每个修复的可观测验证指标（首 token 延迟/事件流时间戳/配额消耗）\n"
                "D. 注入/合成策略是否要改（让模型稳定输出工具调用块而非方案文本），怎么改\n"
                "中文 900 字内。"
            ),
        }
    ],
    "attachments": [
        {"name": "loop_intel_bundle.csv", "path": csv_path, "content_type": "text/csv"}
    ],
}

req = urllib.request.Request(
    "http://127.0.0.1:8787/v1/chat/completions",
    data=json.dumps(body).encode("utf-8"),
    headers={"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"},
)
try:
    r = json.loads(urllib.request.urlopen(req, timeout=600).read().decode("utf-8"))
    text = r["choices"][0]["message"]["content"]
    open(r'C:\Users\Administrator\notion2api\research\loop_round7_plan.txt', 'w', encoding='utf-8').write(text)
    print(text)
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:500])