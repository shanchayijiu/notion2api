import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是规划员。项目 notion2api（GALIAIS Go 框架，Notion AI → OpenAI 兼容桥）开启循环工作流第 N 轮，目标：**验收标准 v4 P0 剩余 unknown 全部关闭**（INV-03 码点守恒账本、INV-06 分块不变性 fuzz、INV-15 通道不回灌、INV-13 六类终止路径、T-16 反向门禁 8 mutant、REQ-DEP-04 部署一致性留痕），产出 v4_consistency_report.json verdict != invalid，且 unknown 尽量清零（需 E3 的除外）。

现有实现关键事实（别推测，按此规划）：
1. 净化链：`notion_client.go:148 sanitizeAssistantVisibleText` = TrimSpace(BOM) → cleanAllLangTags → stripToolActionBlocks → trimTrailingIncompleteCitation → <lang 前缀兜底 → leadingLangTagPattern 循环。`stripToolActionBlocks`（notion_client.go:171）处理 ```json fence 完整/未闭合、`stripUnclosedToolCallXML`（:227）处理 <tool_call> 未闭合。
2. 流式半截标记缓冲：`tool_bridge.go:796 toolStreamSieve`（feed/flush/findBlockEndOn/startsToolPrefixAtBoundaryOn，工具前缀表 toolSievePrefixes 12 项，超限 2048B/40 片/500ms 降级吐出）。
3. 流式 tool_calls 增量分片（REQ-TOOL-04）已实现（main.go:2369-2456：role 首片 → 工具首片 index/id/type/name/arguments="" → 128B 增量片 → finish=tool_calls → usage 片 → DONE），黄金测试 TestStreamToolCallProtocol 已过。chunk 构造 openai.go:1337 buildChatStreamChunk（len(usage)>0 才带 usage 字段——注意：中间 chunk 是"省略"usage 而非"置 null"，REQ-STR-08 要求显式 null）。
4. 现有 v4 测试：v4_p0_test.go（T-07 黑名单扫描/标记剥离/指纹头/synthesizer/流式工具协议/sieve 单测/上游中断 error 映射）。
5. 终止路径：main.go 流式只有"正常结束 → stop 片"与"上游中断 → error 行 + DONE"两条显式路径；其余（客户端取消/内部异常/超时）未逐类验证（INV-13 partial）。
6. 非 git 仓库；无 CI；测试 `go test ./internal/app -count=1` 当前全绿。
7. 码点守恒账本（INV-03）：净化器目前无账本。规划用最小侵入方案：包级 `sanitizeLedgerRecorder` hook（nil 时零开销），在 sanitize 各丢弃点上报 (rule-id, dropped 码点文本)，测试模式开启、重建比对。

请输出（中文，600 字内）：
A. 本轮执行清单（按"快速赢→硬骨头"排序，每项给出：做什么、放哪个文件、验收断言）
B. 每个 mutant（T-16 八项 a-h）对应哪个测试会变红（若现有测试杀不掉，给出新测试名与断言点）
C. INV-03 账本的最小侵入设计（hook 点清单：哪些丢弃点要上报、rule-id 命名）
D. 风险提示：哪些改动可能动到保护区（notion_client.go/main.go），怎么避免回归
E. review 轮你的检查清单（8 条以内）""",
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
