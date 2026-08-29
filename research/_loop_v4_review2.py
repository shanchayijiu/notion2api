import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是审查员，循环工作流第 3 轮。上轮 8 项阻塞已全部落地，请验三张表 + P0 全绿后判定是否仍有阻塞项。

【上轮阻塞项落地清单】
1. hold-back 有界：新增 TestSieveHoldBackBounded（20 片 partial 尾部持续推进、hold < maxPrefix、flush 释放）+ TestSieveHoldBackFlushedOnEOS。
2. fence 白名单改结构规则：findBlockEndOn 与 stripToolActionBlocks 均改为"```+任意字母数字标识=开标记，空/换行/空白=闭合"；新增 TestSieveFenceAnyLanguageHeader（```rust 工具块被剥）+ TestSieveFenceEmptyHeaderIsClose。
3. JSON 配对字符串内花括号：TestSieveJSONBracesInString（`{"a":"}"}` 与转义引号用例）。
4. usage 门控：原实现已门控（仅 include_usage 时发 usage 片），补 TestStreamNoUsageKeyWhenNotRequested 回归（未请求→全流无 usage 键）。
5. INV-15 负向用例：TestINV15NegativeBackflowCase（故意回灌被净化 + 账本 SAN-TOOL-BLOCK 命中断言）。
6. fuzz 种子固化：4 个泄漏 fixture 写入 internal/app/testdata/leak_*.txt + TestINV06PinnedLeakFixtures（全单切点重放 == golden，零标记泄漏）。
7. INV-03 强化：逐 rule 计数断言 + 重复码点对抗 fixture（TestINV03PerRuleCountsAndDuplicates）+ SAN-INVALID-UTF8 规则与测试（非法序列替换记账，守恒定义在合法 UTF-8 域，如实标注）。
8. mutant 矩阵 + 3 新 mutant：m-i hold 无界 / m-j JSON 字符串花括号 / m-k usage 门控 —— 11/11 全杀，mutant→不变量矩阵写入报告。

【pinned fixture 额外实锤（上轮未预见的第 5、6 个泄漏）】
- 流式 sieve 与非流式 sanitize 剥离不一致：xml 方言块（<tool_call>...</tool_call>）流式保留、非流式剥离 → 统一为 xml 块完整即丢弃 + 行内 XML 标记任意位置匹配（TestINV06PinnedLeakFixtures 抓出）。
- fence 开标记漏配：leak_2 跨片断裂场景。

【当前证据】
- go test ./internal/app -count=1 全绿（连跑 2 次）
- 报告：19 pass / 3 unknown（INV-02/09/12 E3 级 P2 范围）/ 0 fail，verdict=insufficient-evidence
- mutant 11/11（scripts/mutant_gate.ps1）
- 服务已重建+重启，healthz 指纹 64674c7e2d97-v1-1787649566，session_ready=true
- reproCommands 表已写入报告

【审查清单】
1. 三张表（mutant→不变量矩阵、seed corpus、pass 命令+证据表）是否齐全可信？
2. 各修复是否有"红灯先行"证据（先失败后转绿）？哪些属于实现修正（须 testChanges 留痕）？
3. 新的 xml 块完整即丢弃语义是否有误伤风险（用户文本里 <tool_call>...</tool_call> 字面量）？与非流式是否真正一致？
4. hold-back 有界性证明是否充分（无界滞留、TTFT 延迟）？
5. 剩余阻塞项？还是可进入下一目标（E3 级金丝雀 / 卡点 A 多轮工具循环）？

输出：判定 + 阻塞项（若有）+ 下一步指令，中文 500 字内。""",
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
    print(r["choices"][0]["message"]["content"])
except urllib.error.HTTPError as e:
    print("HTTP", e.code, ":", e.read().decode("utf-8")[:300])