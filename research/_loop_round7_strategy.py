import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": (
                "你是本项目架构顾问（循环工作流第 7 轮）。先答两个方法问题，再给修复方案。中文 700 字内。\n\n"
                "## 方法问题 A：本轮情报喂法怎么选（用户要求：不能把大段代码塞进你的输入，必须让你拿到足够信息不猜）\n"
                "事实：① 附件上传（Notion AI 读附件）本机网络不可行（S3 直传被墙/代理掐断，实测 EOF）；② 工作区页面建法（saveTransactionsFanout 建 page block）服务端已有事务接口但未实现页面创建，实现成本 1-2 小时；③ 历史轮次用浓缩 prompt（关键函数名+少量代码 3-5KB）成功过。\n"
                "选哪个？为什么？给具体做法。\n\n"
                "## 方法问题 B：账号配额治理（用户实测'回复极慢/无法回复'的根因之一）\n"
                "事实：每号 ~5 推理/小时后 quota-exhausted 自动轮换；轮换需 createspace（每日上限 6 个，429 不可绕）；429 后轮换死锁 180s 超时 → 该号后续全部 502 直到次日；本机只有 2 个健康号。\n"
                "怎么治理？(注册机可产新号 adguard；建议量化消耗/限流策略/排队)\n\n"
                "## 修复方案 C（基于前 6 轮已确认事实，不猜代码细节）\n"
                "用户三大痛点：回复极慢/无流式/工具调用烂。已确认：① /v1/messages 流式已改为实时管线（liveSSEWriter→pipe→转换器，未实测首 token）；② 认知重构注入把请求撑到 ~96KB（CC 完整 system + 注入），上游推理慢；③ 模型输出随机（调用块 vs 方案文本）；④ CC 收到 Agent tool_use 会创建子代理（异步、耗配额）。\n"
                "给 A/B/C 三个问题的方案（最小改动、可验证指标）。"
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
    open(r'C:\Users\Administrator\notion2api\research\loop_round7_strategy.txt', 'w', encoding='utf-8').write(text)
    print(text)
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:400])