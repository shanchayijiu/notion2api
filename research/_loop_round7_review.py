import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": (
                "你是本项目架构顾问（循环工作流第 7 轮 review）。中文 600 字内。\n\n"
                "## 本轮已执行（按你上轮规划）\n"
                "B1 熔断：createspace 429 第二次立即返回（删退避），外层标 24h 冷却摘除；rotateHTTPTimeout 180s→60s。已生效（日志确认 60s 超时不再 180s 死锁）\n"
                "C3 屏蔽子代理：/v1/messages 工具列表过滤 Agent/SendMessage/AddTaskNotificationTool（CC 只能输出常规工具，本地执行）\n"
                "C1 条件注入：续轮（有工具回填）ToolBridgeSection 换 buildToolBridgeSummary（≤5KB 摘要版）\n"
                "C2 埋点+心跳：converter 首 chunk TTFT 日志 + 15s ping。实测 TTFT 1.5s（首字节/首事件同步，流式实时已生效）\n"
                "另：附件类型校验前置 400（不再烧账号）；S3 上传直连（本机网络不可行，附件方案放弃）\n\n"
                "## 新账号流程已跑通 3 次（n0022/23/24：adguard 注册→导入→probe 复制→rotate→activate）\n\n"
                "## 实测结果（CC 经 /v1/messages）\n"
                "任务：写 hello2.py 输出 Hello world 并用 python 运行。\n"
                "CC 输出：纯文档式方案文本（\"Once the client reports the write succeeded, the second operation executes it. **Bash** runs the command in Git Bash...\"），"
                "无 JSON 调用块、无工具执行、文件未创建。\n"
                "机理：模型仍处\"文档模式\"输出方案文本（未输出调用块）→ extractToolCalls 空 → synthesizeToolCall 空 → synthesizeTaskCall 需要 Agent 工具（已被 C3 屏蔽）→ 无兜底 → 纯文本。\n\n"
                "## 决策请求\n"
                "A. C3 与合成兜底的关系怎么处理（保留屏蔽 vs 放开 Agent vs 改合成目标）？\n"
                "B. 若改为\"合成具体工具\"（Write/Bash）：从方案文本提取 file_path/content/command 的策略（模型方案文本含文件名 hello2.py、代码块、运行命令），给具体提取规则\n"
                "C. 合成触发条件放宽后怎么防误伤（普通问答不合成）\n"
                "D. 本轮四项改动是否有需要回退/修正的（引用机理）"
            ),
        }
    ],
}

req = urllib.request.Request(
    "http://127.0.0.1:8787/v1/chat/completions",
    data=json.dumps(body).encode("utf-8"),
    headers={"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"},
)
try:
    r = json.loads(urllib.request.urlopen(req, timeout=300).read().decode("utf-8"))
    text = r["choices"][0]["message"]["content"]
    open(r'C:\Users\Administrator\notion2api\research\loop_round7_review.txt', 'w', encoding='utf-8').write(text)
    print(text)
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:400])