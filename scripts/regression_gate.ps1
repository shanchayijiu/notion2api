# regression_gate.ps1 — 回归门禁（CI 必过，review round 4 指令 #4）
# 内容：全量测试 + 反向门禁 11/11 mutant + pinned 泄漏 fixture
# 用法:  pwsh -NoProfile -File scripts/regression_gate.ps1
# 任一环节失败 → exit 1

$ErrorActionPreference = "Continue"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
$failed = $false

Write-Host "=== 1/3 go vet ==="
& go vet ./internal/app 2>&1 | Out-String | Write-Host
if ($LASTEXITCODE -ne 0) { $failed = $true }

Write-Host "=== 2/3 go test 全量（含 INV-06 fuzz / pinned fixtures / INV-03 账本 / 终止矩阵 / usage）==="
& go test ./internal/app -count=1 2>&1 | Out-String | Write-Host
if ($LASTEXITCODE -ne 0) { $failed = $true }

Write-Host "=== 3/3 T-16 反向门禁（mutant 11/11 必须全杀）==="
& pwsh -NoProfile -File scripts/mutant_gate.ps1 2>&1 | Out-String | Write-Host
if ($LASTEXITCODE -ne 0) { $failed = $true }

Pop-Location
if ($failed) {
    Write-Host "回归门禁 FAILED"
    exit 1
}
Write-Host "回归门禁 PASS（vet + 全量测试 + mutant 11/11）"
exit 0