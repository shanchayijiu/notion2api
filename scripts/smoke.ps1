# smoke.ps1 — P0 主路径回归（每次改动必跑）
# 用法: powershell -ExecutionPolicy Bypass -File scripts/smoke.ps1 [BASE_URL] [API_KEY]
$ErrorActionPreference = "Stop"
$base = if ($args.Count -gt 0) { $args[0] } else { "http://127.0.0.1:8787" }
$key  = if ($args.Count -gt 1) { $args[1] } else { "sk-notion2api-dev" }
$h = @{ "Authorization" = "Bearer $key"; "Content-Type" = "application/json" }
$fail = 0

function Check($name, $cond, $detail) {
    if ($cond) { Write-Output "[PASS] $name" }
    else { $script:fail++; Write-Output "[FAIL] $name :: $detail" }
}

# 1. healthz
try {
    $hz = Invoke-RestMethod -Uri "$base/healthz" -TimeoutSec 10
    Check "healthz ok" ($hz.ok -eq $true) "body=$hz"
} catch { Check "healthz ok" $false $_.Exception.Message }

# 2. models
try {
    $m = Invoke-RestMethod -Uri "$base/v1/models" -Headers $h -TimeoutSec 15
    Check "models >= 20" ($m.data.Count -ge 20) "count=$($m.data.Count)"
} catch { Check "models >= 20" $false $_.Exception.Message }

# 3. 非流式
try {
    $body = '{"model":"gpt-5.4","messages":[{"role":"user","content":"Reply with exactly: OK"}],"stream":false}'
    $r = Invoke-RestMethod -Uri "$base/v1/chat/completions" -Method Post -Headers $h -Body $body -TimeoutSec 180
    $c = $r.choices[0].message.content
    Check "non-stream content=OK" ($c -eq "OK") "content=$c"
    Check "non-stream no lang tags" ($c -notmatch "<lang") "content=$c"
} catch { Check "non-stream" $false $_.Exception.Message }

# 4. 流式
try {
    $body = '{"model":"gpt-5.4","messages":[{"role":"user","content":"Reply with exactly: OK"}],"stream":true}'
    $resp = Invoke-WebRequest -Uri "$base/v1/chat/completions" -Method Post -Headers $h -Body $body -TimeoutSec 180
    $lines = $resp.Content -split "`n"
    $content = ($lines | Where-Object { $_ -match 'delta.*content' } | ForEach-Object {
        if ($_ -match '"content":"([^"]*)"') { $matches[1] }
    }) -join ""
    $done = ($lines | Where-Object { $_ -match "\[DONE\]" }).Count -gt 0
    Check "stream ends with [DONE]" $done "lines=$($lines.Count)"
    Check "stream content=OK" ($content -eq "OK") "content=$content"
} catch { Check "stream" $false $_.Exception.Message }

# 5. 最新模型表（claude-opus5 -> agave-flan）
try {
    $body = '{"model":"opus-5","messages":[{"role":"user","content":"Reply with exactly: OK"}],"stream":false}'
    $r = Invoke-RestMethod -Uri "$base/v1/chat/completions" -Method Post -Headers $h -Body $body -TimeoutSec 180
    $nm = $r.notion_trace.notion_model
    Check "opus5 notion_model=agave-flan" ($nm -eq "agave-flan") "notion_model=$nm"
} catch { Check "opus5 model" $false $_.Exception.Message }

if ($fail -gt 0) { Write-Output "SMOKE_RESULT: $fail FAILED"; exit 1 }
Write-Output "SMOKE_RESULT: ALL PASS"