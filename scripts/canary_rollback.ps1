# canary_rollback.ps1 — 一键回滚（review round 4 指令 #3）
# 触发条件（见 _runtime/canary/canary_config.json rollbackTriggers）：
#   指纹漂移 / 连续 3 次运行失败 / INV-09 串话 / INV-02 命中
# 动作：停止金丝雀计划任务 → 写 ALERT 终态 → 输出证据摘要
# 用法:  pwsh -NoProfile -File scripts/canary_rollback.ps1 [-reason <text>]

param([string]$reason = "manual")

$root = Split-Path -Parent $PSScriptRoot
$canaryDir = Join-Path $root "_runtime\canary"

Write-Host "=== 停止金丝雀计划任务 ==="
& schtasks /End /TN "notion2api-canary-t09" 2>&1 | Out-String | Write-Host
& schtasks /Delete /TN "notion2api-canary-t09" /F 2>&1 | Out-String | Write-Host

$alert = Join-Path $canaryDir "ALERT"
Add-Content -LiteralPath $alert -Value ("[ROLLBACK] {0} reason={1}" -f (Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz"), $reason) -Encoding UTF8

Write-Host "=== 证据摘要 ==="
$results = Join-Path $canaryDir "T-09_results.jsonl"
if (Test-Path $results) {
    $runs = (Get-Content $results | Where-Object { $_ -ne "" }).Count
    Write-Host ("已采集运行数: {0}" -f $runs)
} else {
    Write-Host "无采集数据"
}
$fixDir = Join-Path $canaryDir "unknown_fixtures"
if (Test-Path $fixDir) {
    $hits = (Get-ChildItem $fixDir -File -ErrorAction SilentlyContinue).Count
    Write-Host ("INV-02 候选 fixture: {0}" -f $hits)
}
Write-Host "=== 回滚完成：金丝雀已停，基线冻结解除；恢复需重建产物→重启→重新冻结基线→重装任务 ==="
exit 0