#!/usr/bin/env python3
"""canary_t09.py — E3 金丝雀（验收标准 v4 T-09）：生产常驻定时探测固定用例集。

每次运行：
1. 指纹校验（healthz binary_sha256 == 基线；不一致 → ALERT + 停止）
2. INV-12 真流式时序采样（TTFB/块间间隔/跳度/块数比）→ 累积 T-09_results.jsonl
3. INV-09 无状态：并发 nonce 隔离探针 + 复读探针
4. INV-02 未知标记：检查服务侧候选 fixture 目录新增命中

用法: python research/canary_t09.py [--once]
调度: Windows 计划任务每 10 分钟（观察窗 24h，样本 ≥200）
产出: _runtime/canary/T-09_results.jsonl（每运行一行 JSON）
"""
import argparse
import json
import os
import sys
import time
import urllib.request
import urllib.error
import threading
from datetime import datetime, timezone

sys.stdout.reconfigure(encoding="utf-8")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CANARY_DIR = os.path.join(ROOT, "_runtime", "canary")
CONFIG_PATH = os.path.join(CANARY_DIR, "canary_config.json")
RESULTS_PATH = os.path.join(CANARY_DIR, "T-09_results.jsonl")
ALERT_PATH = os.path.join(CANARY_DIR, "ALERT")

cfg = json.load(open(CONFIG_PATH, encoding="utf-8"))
BASE = cfg["endpoints"]["base"]
KEY = cfg["endpoints"]["apiKey"]
BASELINE = cfg["baseline"]["binarySha256"]


def now_iso():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def alert(msg):
    with open(ALERT_PATH, "a", encoding="utf-8") as f:
        f.write(f"[{now_iso()}] {msg}\n")
    print(f"[ALERT] {msg}", flush=True)


def clear_alert():
    if os.path.exists(ALERT_PATH):
        os.remove(ALERT_PATH)


def healthz():
    req = urllib.request.Request(BASE + "/healthz", headers={"Authorization": f"Bearer {KEY}"})
    return json.loads(urllib.request.urlopen(req, timeout=15).read().decode())


def stream_chat(prompt, timeout=180, retries=1):
    """流式请求，返回 (内容, 时序字典)。429/瞬时错误退避重试。"""
    body = {
        "model": "gpt-5.4",
        "stream": True,
        "messages": [{"role": "user", "content": prompt}],
    }
    last_err = None
    for attempt in range(retries + 1):
        try:
            req = urllib.request.Request(
                BASE + "/v1/chat/completions",
                data=json.dumps(body).encode("utf-8"),
                headers={"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"},
            )
            t0 = time.monotonic()
            resp = urllib.request.urlopen(req, timeout=timeout)
            content = ""
            chunk_times = []
            chunk_count = 0
            ttfb = None
            prev = None
            buf = b""
            while True:
                raw = resp.read(4096)
                if not raw:
                    break
                now = time.monotonic()
                if ttfb is None:
                    ttfb = now - t0
                buf += raw
                while b"\n" in buf:
                    line, buf = buf.split(b"\n", 1)
                    if line.startswith(b"data: "):
                        data = line[6:].strip()
                        if data == b"[DONE]":
                            continue
                        try:
                            chunk = json.loads(data)
                        except Exception:
                            continue
                        choices = chunk.get("choices") or []
                        if choices and "delta" in choices[0]:
                            delta = choices[0]["delta"]
                            if isinstance(delta.get("content"), str):
                                content += delta["content"]
                                if prev is not None:
                                    chunk_times.append(now - prev)
                                prev = now
                                chunk_count += 1
            total = time.monotonic() - t0
            if len(chunk_times) > 0:
                inter_p95 = sorted(chunk_times)[int(len(chunk_times) * 0.95 - 0.01)]
            else:
                inter_p95 = None
            timing = {
                "ttfbMs": round(ttfb * 1000, 1) if ttfb is not None else None,
                "totalMs": round(total * 1000, 1),
                "chunks": chunk_count,
                "interChunkP95Ms": round(inter_p95 * 1000, 1) if inter_p95 is not None else None,
                "outputBytes": len(content.encode("utf-8")),
                "jumpiness": round(len(content.encode("utf-8")) / total / 1000, 3) if total > 0 else 0,
            }
            return content, timing
        except urllib.error.HTTPError as e:
            last_err = f"HTTP {e.code}: {e.read().decode()[:150]}"
        except Exception as e:
            last_err = str(e)[:200]
        if attempt < retries:
            time.sleep(10)
    raise RuntimeError(last_err or "unknown error")


def nonce_probe(nonce):
    """单发 nonce 隔离请求（线程内执行）。"""
    prompt = f"请回复短语'口令已记录'并在末尾原样输出这串口令：{nonce}"
    try:
        content, timing = stream_chat(prompt)
        return {"nonce": nonce, "content": content, "ok": True, "timing": timing}
    except Exception as e:
        return {"nonce": nonce, "content": "", "ok": False, "error": str(e)[:200]}


def run():
    run_id = int(time.time())
    result = {"run": run_id, "ts": now_iso(), "checks": {}}

    def probe(label, fn):
        """单探针兜底：失败记错误不崩溃，绝不让单次失败杀死整轮采集。"""
        try:
            return fn(), None
        except Exception as e:
            return None, f"{label}: {str(e)[:200]}"

    # 1) 指纹校验
    _, hz_err = probe("healthz", healthz)
    if hz_err:
        alert(f"healthz 探测失败: {hz_err}")
        result["error"] = hz_err
        write_result(result)
        return
    hz = healthz()
    sha = hz.get("binary_sha256", "")
    fp_ok = sha.startswith(BASELINE)
    result["checks"]["fingerprint"] = {"ok": fp_ok, "binarySha256": sha, "activeAccount": hz.get("active_account"), "sessionReady": hz.get("session_ready")}
    if not fp_ok:
        alert(f"基线漂移: healthz sha={sha} != baseline {BASELINE}（有意重建后需重新冻结基线）")
        write_result(result)
        return

    # 2/3) 交替排班（单号限流约束，降负载 ~40%）：
    #   偶数 run：INV-12 时序采样 ×2
    #   奇数 run：INV-09 nonce 隔离 + 复读
    # 账号故障期（502/429）探针失败仅记录，不告警（真实串话才告警）
    if run_id % 2 == 0:
        timing_samples = []
        for i in range(2):
            _, timing = probe(f"timing-{i+1}", lambda: stream_chat("用一句话介绍你自己的功能。", retries=1)[1])
            if timing is not None:
                timing_samples.append(timing)
            time.sleep(6)
        result["checks"]["timing"] = {"ok": len(timing_samples) == 2, "samples": timing_samples}
        result["checks"]["isolation"] = {"ok": True, "skipped": True}
        result["checks"]["repeat"] = {"ok": True, "skipped": True}
    else:
        result["checks"]["timing"] = {"ok": True, "skipped": True, "samples": []}
        n1, n2 = f"CANARY-{run_id}-A", f"CANARY-{run_id}-B"
        p1, p2 = {}, {}
        p1["content"], e1 = probe("isolation-a", lambda: stream_chat(f"请回复短语'口令已记录'并在末尾原样输出这串口令：{n1}", retries=1)[0])
        time.sleep(6)
        p2["content"], e2 = probe("isolation-b", lambda: stream_chat(f"请回复短语'口令已记录'并在末尾原样输出这串口令：{n2}", retries=1)[0])
        time.sleep(6)
        both_ok = e1 is None and e2 is None
        real_crosstalk = both_ok and (n1 not in (p1.get("content") or "") or n2 in (p1.get("content") or "") or n2 not in (p2.get("content") or "") or n1 in (p2.get("content") or ""))
        result["checks"]["isolation"] = {"ok": not real_crosstalk, "executed": both_ok, "err": e1 or e2, "p1ContainsN1": n1 in (p1.get("content") or ""), "p1ContainsN2": n2 in (p1.get("content") or ""), "p2ContainsN2": n2 in (p2.get("content") or ""), "p2ContainsN1": n1 in (p2.get("content") or "")}
        if real_crosstalk:
            alert(f"INV-09 真实串话命中: {json.dumps(result['checks']['isolation'], ensure_ascii=False)}")
        repeat_content, e3 = probe("repeat", lambda: stream_chat(f"请回复短语'口令已记录'并在末尾原样输出这串口令：{n1}", retries=1)[0])
        repeat_crosstalk = e3 is None and n2 in (repeat_content or "")
        result["checks"]["repeat"] = {"ok": not repeat_crosstalk, "executed": e3 is None, "err": e3, "repeatContainsN2": n2 in (repeat_content or "")}
        if repeat_crosstalk:
            alert(f"INV-09 复读串话命中: {json.dumps(result['checks']['repeat'], ensure_ascii=False)}")

    # 4) INV-02 未知标记：检查候选 fixture 目录
    fix_dir = os.path.join(ROOT, cfg["unknownFixturesDir"])
    hits = []
    if os.path.isdir(fix_dir):
        for f in sorted(os.listdir(fix_dir)):
            hits.append({"file": f, "mtime": datetime.fromtimestamp(os.path.getmtime(os.path.join(fix_dir, f))).strftime("%Y-%m-%dT%H:%M:%S")})
    result["checks"]["unknownMarkers"] = {"hits": hits, "ok": len(hits) == 0}
    if hits:
        alert(f"INV-02 未知标记命中 {len(hits)} 个候选 fixture")

    write_result(result)


def rows_snapshot():
    rows = []
    if os.path.exists(RESULTS_PATH):
        with open(RESULTS_PATH, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line:
                    rows.append(json.loads(line))
    return rows


def write_result(result):
    with open(RESULTS_PATH, "a", encoding="utf-8") as f:
        f.write(json.dumps(result, ensure_ascii=False) + "\n")
    checks = result.get("checks", {})
    t = checks.get("timing", {})
    i = checks.get("isolation", {})
    print(f"run {result['run']} done: fingerprint={checks.get('fingerprint', {}).get('ok')} timing={len(t.get('samples', []))} isolation_exec={i.get('executed', 'skip')} iso_ok={i.get('ok')} err={result.get('error', 'none')}")


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--once", action="store_true", help="只跑一次（调试）")
    args = ap.parse_args()
    os.makedirs(CANARY_DIR, exist_ok=True)
    try:
        run()
    except Exception as e:
        # 兜底：任何未预期异常都要落盘（exit 0，避免计划任务报错中断采集）
        alert(f"canary 未预期异常: {e}")
        write_result({"run": int(time.time()), "ts": now_iso(), "error": f"unexpected: {e}"})