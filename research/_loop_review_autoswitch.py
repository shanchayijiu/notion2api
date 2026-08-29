import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

ctx = """你是审查者（循环工作流第 4 步：review）。我是 notion2api 作者（Go，Notion AI → OpenAI 兼容桥，127.0.0.1:8787）。

【刚刚完成的改动：账号自动检测 + 自动切换】
背景：主号被上游标记（runInferenceTranscript 只回 3 行 scaffold、从不启动推理 → 后续 syncThread 黑洞 → 请求挂死 150s+）。用户要求"自动检测，出问题就自动切账号，而不是出问题再去检查"。

改动清单：
1. notion_client.go：ndjsonParseResult 新增 HasAgentInference 字段；ndjsonTranscriptState.result() 遍历 Steps 统计是否有 type=="agent-inference" 的步骤。
2. 新增哨兵错误 errAccountStarved（"upstream did not start inference for this account (account may be limited); switching account"）。
3. RunPrompt / RunPromptStream：在原有 parseErr 处理之后、调用 loadFinalAnswerOnce 之前插入——若 parseErr==nil 且 !parsed.HasAgentInference 且无文本 → 直接 return errAccountStarved（跳过 loadFinalAnswerOnce/poll 长等待）。
4. loadFinalAnswerOnce 的 syncThread/syncThreadMessages 包 bestEffortContext(20s) 黑洞检测（accountSyncBlackholeTimeout）。
5. pollFinalAnswer / pollFinalAnswerStream 整体 bestEffortContext(45s)（accountPollBlackholeTimeout）。
6. account_pool.go：markAccountDispatchFailure 现在设置 CooldownUntil = now + computeAccountCooldown(account, retryable)（不再清空）；accountDispatchEligible 增加冷却检查（accountCooldownActive）。冷却 30s-30min 随失败次数增长，到期自动恢复。
7. dispatch 失败自动换下一个候选（已有逻辑）：errAccountStarved 非 retryable → markAccountDispatchFailure(非retryable) → 设 cooldown → 下一候选；emittedAny==false 时才换。

【请审查并输出】
1. 这套机制能否达成"上游挂掉/被标记 → 快速失败 → 自动切下一个账号"？指出任何会**导致仍挂死、或无限重试、或漏切**的漏洞（例如：active 账号优先导致仍先试坏号耗尽、流式已发部分文本后不切换、只有 1 个账号时、cooldown 与 active 排序交互、errAccountStarved 被误判为非故障等）。
2. 黑洞检测超时（20s/45s）取值是否合理（正常账号 sync 实测 0.3s 返回）。
3. 是否需要补：失败冷却后"自愈复测"、健康检查端点、或账号连续失败自动停用（disabled）。
4. 按"会阻断自动切换生效"严重程度排序的必修复项清单（每条：漏洞/影响/修法/优先级），中文，900 字内。"""

body = {
    "model": "opus-5",
    "messages": [{"role": "user", "content": ctx}],
    "max_tokens": 1200,
}

req = urllib.request.Request(
    "http://127.0.0.1:8787/v1/chat/completions",
    data=json.dumps(body).encode("utf-8"),
    headers={"Authorization": "Bearer sk-notion2api-dev", "Content-Type": "application/json"},
)
try:
    r = json.loads(urllib.request.urlopen(req, timeout=300).read().decode("utf-8"))
    print(r["choices"][0]["message"]["content"])
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:400])