# scripts/test.ps1 — one-shot verification for Windows (equivalent to `make test`).
# Usage: pwsh -File scripts/test.ps1
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

Write-Host "[1/4] go vet ./..." -ForegroundColor Cyan
go vet ./...

Write-Host "[2/4] go build ./..." -ForegroundColor Cyan
go build ./...

Write-Host "[3/4] go test ./... -count=1" -ForegroundColor Cyan
go test ./... -count=1

Write-Host "[4/4] OpenAI Python SDK compatibility smoke" -ForegroundColor Cyan
python -c "import openai" 2>$null
if ($LASTEXITCODE -eq 0) {
    go test ./internal/app -count=1 -run TestOpenAIPythonSDKCompatibility -v
} else {
    Write-Host "python/openai not available - skipping SDK smoke (auto-skipped by the test too)" -ForegroundColor Yellow
}

Write-Host "ALL GREEN" -ForegroundColor Green
