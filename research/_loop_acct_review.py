import io, subprocess, sys

def rd(p):
    try:
        return open(p, encoding="utf-8").read()
    except Exception as e:
        return "<<read error: %s>>" % e

reg = rd("C:/Users/Administrator/notion2api/internal/app/register_provider.go")
main = rd("C:/Users/Administrator/notion2api/internal/app/main.go")
disp = rd("C:/Users/Administrator/notion2api/internal/app/request_dispatch.go")
import json
cfg = json.load(open("C:/Users/Administrator/notion2api/config.json", encoding="utf-8"))

# excerpt main.go SaveAndApply/ApplyConfig/Snapshot
def excerpt(text, start_marker, end_marker=None, span=0):
    i = text.find(start_marker)
    if i < 0:
        return "<<not found: %s>>" % start_marker
    if end_marker:
        j = text.find(end_marker, i+len(start_marker))
        if j < 0:
            return text[i:i+4000]
        return text[i:j+len(end_marker)]
    return text[i:i+4000]

main_exc = excerpt(main, "func (s *ServerState) ApplyConfig(cfg AppConfig)", "func (s *ServerState) SaveAndApply")
main_exc += "\n\n" + excerpt(main, "func (s *ServerState) SaveAndApply(cfg AppConfig)", "func (s *ServerState) conversationPersistenceStore")
main_exc += "\n\n" + excerpt(main, "func (s *ServerState) Snapshot()", "func (s *ServerState) updateSnapshotBundleLocked")
disp_exc = excerpt(disp, "ctx, cancel := context.WithTimeout(r.Context(), timeout)", "emittedAny := false")
disp_exc += "\n\n" + excerpt(disp, "if isDispatchContextAbort(ctx, err) {", "account = markAccountDispatchFailure(account, time.Now(), err, retryable)")

prompt = """你是 notion2api（Go，Notion AI → OpenAI 兼容桥 127.0.0.1:8787）的代码审查者。下面贴了真实代码，请据此审查「账号功能」改动。不要改代码，只输出结论。

==== register_provider.go (RegisterNewAccount + findAccountCreatedSince) ====
%s

==== main.go: ApplyConfig / SaveAndApply / Snapshot ====
%s

==== request_dispatch.go: 候选解析+snap读取+starved处理 ====
%s

==== config.json model_aliases ====
%s

改动目标：注册机产号后能【自动入池】（无需手动 admin 导入），且修复 GBK/非0退出导致注册失败；并为旧客户端 gpt-5.2 加别名到 gpt-5.4。

请输出（中文，900字内）：
1. 注册自动入池是否真正生效？重点核对：RegisterNewAccount 调 SaveAndApply 后，dispatch 是否真能选到新号（snap 是否刷新、SaveAndApply 是否可能因 validateConfiguredAPIKey 失败而回滚导致没入池）；findAccountCreatedSince 的时间窗是否会误抓旧号或漏抓新号；去重逻辑是否完整。
2. 账号功能还有哪些 P0 漏洞（dispatch 冷却/切换边界、半开探测与 active 排序冲突、注册并发、账号失效后未自动补充、alias 目标在切换账号后不存在等）？给 文件:行号 + 修法。
3. 最终判定：账号功能是否「生产可用」？还差的 1-3 件事（按优先级）。""" % (reg, main_exc, disp_exc, json.dumps(cfg.get("model_aliases"), ensure_ascii=False))

open("C:/Users/Administrator/notion2api/research/_acct_prompt.txt", "w", encoding="utf-8").write(prompt)
print("prompt written")