import json, sys, urllib.request, urllib.error

sys.stdout.reconfigure(encoding="utf-8")

body = {
    "model": "opus-5",
    "messages": [
        {
            "role": "user",
            "content": """你是审查员，循环工作流第 2 轮（目标：验收标准 v4 P0 收口）。执行已完成，请按清单审查并判定阻塞项。

【本轮执行内容】
1. INV-03 码点守恒账本：sanitize_ledger.go 新增，所有丢弃点按 rule-id 上报（SAN-TRIM-BOM/TRIM/LANG/LANG-LEAD/LANG-UNCLOSED/TOOL-BLOCK/TOOL-FENCE-UNCLOSED/TOOL-XML/TOOL-XML-UNCLOSED/CITE-TAIL）；测试断言逐码点多重集守恒（input==output+drops，每码点恰好一次），7 fixture 全过。注意：账本按多重集断言而非顺序重建（净化各阶段交错丢弃无位置信息），已如实标注。
2. INV-06 分块 fuzz：抓到并修复 4 个 sieve 真实泄漏——①前导文本黏进块切片致工具块泄漏；②标记跨片断裂（```/json）泄漏→引入 v4 S2 hold-back；③超限闸把整段流尾当块释放→先判闭合再判超限；④findBlockEndOn 跨方言取闭合（XML 块后跟 fence 块被错切）→块方言跟踪（fence/xml/json）。feed() 重构为状态机循环。TestSieveBlockOverflowDegrades 语义修正（闭合工具块必丢弃，未闭合超限降级释放）。
3. INV-13 六类终止矩阵：EOS/stop 序列（新增 stop 参数支持，S4 缓冲模式）/上游中断（error 行+DONE 不映射 stop）/内部异常（首字节前 HTTP 5xx）/客户端取消（live server，无 DONE+上游取消传播）/缓冲超限。长度上限如实标 N/A（上游无长度语义）。
4. REQ-STR-08 usage null：buildChatStreamChunk usage 语义 nil=省略/空 map=显式 null/非空=值；plain/stop/tool/burst 四路径统一"finish 片后独立 usage 片（空 choices）"。
5. REQ-DEP-04/INV-16：TestBuildFingerprintMatchesRunningBinary（os.Executable sha256 == healthz 指纹）；已重建→重启→探测。
6. T-16 反向门禁：scripts/mutant_gate.ps1，8 mutant 全杀（m-a 中断丢 DONE / m-b fence 漏配 / m-c hold-back 禁用 / m-d 未闭合保留标记 / m-e 不剥工具块 / m-f usage null 省略 / m-g 中断映射 stop / m-h 指纹硬编码）。
7. 报告：19 pass / 3 unknown（INV-02/09/12 E3 级 P2 范围）/ 0 fail，verdict=insufficient-evidence；mutant 8/8；testChanges 4 条留痕。

【审查清单（上轮订立）】
1. 账本口径是否覆盖代理对/组合字符/BOM/非法序列？多重集断言的等价性是否可接受？
2. 每个 pass 是否可复现（命令+证据文件）？
3. 六类终止是否逐类独立用例且断言具体？
4. INV-15 是否有故意回灌的负向用例？
5. fuzz 是否固定种子、失败用例入库？
6. 8 mutant 是否按不变量分散布点、逐个记录杀手？
7. 存活 mutant 登记？存活=门禁缺口？
8. 结论是否三态（pass/fail/unknown）？

重点审：hold-back 与 findBlockEndOn 重构是否引入新漏洞（如 hold-back 无限延迟、fence 语言标识列表漏项、JSON 配对误判）；usage null 改动是否破坏现有客户端（严格 SDK）；stop 缓冲模式的取舍是否合理（牺牲真流式换正确截断）；m-a 与 m-g 是否真正覆盖八个高频漏点（对照 v4 §0 清单 a-h）。

输出：判定 + 阻塞项清单（若有）+ 下一步指令，中文 500 字内。""",
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