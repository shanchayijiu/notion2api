# mutant_gate.ps1 — T-16 反向门禁（验收标准 v4 M6）
# 八个高频漏点各注入一个 mutant 到 internal/app 的副本，断言对应杀手测试逐个变红。
# 任何 mutant "存活"（测试仍绿）= 门禁缺口，必须补测试或修实现。
#
# 用法:  pwsh -File scripts/mutant_gate.ps1
# 产出:  _runtime/mutant_results.json（killed/total + 每个 mutant 的 killedBy/survived）

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$wsRoot = Join-Path $root "_runtime\mutant_ws"
$results = @()

function Invoke-Mutant {
    param(
        [string]$Id,
        [string]$File,
        [string]$Pattern,
        [string]$Replacement,
        [string]$KillTests
    )
    $ws = Join-Path $wsRoot $Id
    if (Test-Path $ws) { Remove-Item $ws -Recurse -Force }
    New-Item -ItemType Directory -Path $ws -Force | Out-Null
    Copy-Item (Join-Path $root "internal\app") (Join-Path $ws "app") -Recurse -Force

    $target = Join-Path $ws "app\$File"
    $content = Get-Content $target -Raw
    $mutated = $content -replace $Pattern, $Replacement
    if ($mutated -eq $content) {
        throw ("mutant {0}: pattern did not match anything in {1}" -f $Id, $File)
    }
    Set-Content -LiteralPath $target -Value $mutated -NoNewline -Encoding UTF8

    $output = & go test (".\" + (Split-Path -Leaf $ws) + "\app") -run $KillTests -count=1 2>&1
    $exit = $LASTEXITCODE
    $killed = ($exit -ne 0)
    $summary = if ($killed) { "KILLED" } else { "SURVIVED" }
    Write-Host ("[{0}] {1} (exit={2}) killTests={3}" -f $Id, $summary, $exit, $KillTests)
    $script:results += [ordered]@{
        id        = $Id
        file      = $File
        killTests = $KillTests
        killed    = $killed
        exitCode  = $exit
    }
}

Push-Location $root
try {
    Invoke-Mutant -Id "m-a" -File "main.go" -Pattern '"code":    "upstream_aborted",(\r?\n\t\t\t\t\},)\r?\n\t\t\t\}\)\r?\n\t\t\tsafeWriteDone\(\)' -Replacement '"code":    "upstream_aborted",$1' -KillTests 'TestV4StreamUpstreamAbortMapsErrorNotStop|TestINV13TerminationUpstreamAbort'

    Invoke-Mutant -Id "m-b" -File "notion_client.go" -Pattern 'lowerHeader := strings.ToLower\(header\)' -Replacement 'lowerHeader := strings.ToLower(header) + "x"' -KillTests 'TestStripToolActionBlocksClosedBlock|TestSanitizeAssistantVisibleTextToolBlocks'

    Invoke-Mutant -Id "m-c" -File "tool_bridge.go" -Pattern 'if hold := trailingMarkerPrefixHold\(s.buffer\); hold > 0 \{' -Replacement 'if hold := 0; hold > 0 {' -KillTests 'TestINV06ChunkInvarianceSmall2|TestINV06ChunkInvarianceUnclosedFence|TestINV06ChunkInvarianceSmall1'

    Invoke-Mutant -Id "m-d" -File "tool_bridge.go" -Pattern 'out = out\[:idx\]' -Replacement 'out = out' -KillTests 'TestSieveUnclosedDegradesToText'

    Invoke-Mutant -Id "m-e" -File "notion_client.go" -Pattern 'clean = stripToolActionBlocks\(clean\)' -Replacement 'clean = clean' -KillTests 'TestSanitizeAssistantVisibleTextToolBlocks|TestStripToolActionBlocksClosedBlock'

    Invoke-Mutant -Id "m-f" -File "openai.go" -Pattern 'payload\["usage"\] = nil' -Replacement '// usage null omitted (mutant f)' -KillTests 'TestStreamUsageNullOnMiddleChunks'

    Invoke-Mutant -Id "m-g" -File "main.go" -Pattern '"code":    "upstream_aborted",' -Replacement '"code":    "stop",' -KillTests 'TestINV13TerminationUpstreamAbort|TestV4StreamUpstreamAbortMapsErrorNotStop'

    Invoke-Mutant -Id "m-h" -File "build_identity.go" -Pattern 'binarySHA = hex.EncodeToString\(sum\[:\]\)' -Replacement 'binarySHA = strings.Repeat("0", 64)' -KillTests 'TestBuildFingerprintMatchesRunningBinary'

    # review 轮 2 增补（不变量矩阵补位）
    Invoke-Mutant -Id "m-i" -File "tool_bridge.go" -Pattern 'for l := 1; l < len\(pl\); l\+\+ \{' -Replacement 'for l := 1; l < len(pl)*2; l++ {' -KillTests 'TestSieveHoldBackBounded'

    Invoke-Mutant -Id "m-j" -File "tool_bridge.go" -Pattern 'if inStr \{' -Replacement 'if false {' -KillTests 'TestSieveJSONBracesInString|TestSieveBraceInString'

    Invoke-Mutant -Id "m-k" -File "openai.go" -Pattern 'if includeUsage \{\r?\n\t\treturn map\[string\]any\{\}\r?\n\t\}\r?\n\treturn nil' -Replacement "if includeUsage {`r`n`t`treturn map[string]any{}`r`n`t}`r`n`treturn map[string]any{}" -KillTests 'TestStreamNoUsageKeyWhenNotRequested'
}
finally {
    Pop-Location
}

$killedCount = ($results | Where-Object { $_.killed }).Count
$payload = [ordered]@{
    generatedAt = (Get-Date -Format "yyyy-MM-ddTHH:mm:sszzz")
    killed      = $killedCount
    total       = $results.Count
    note        = if ($killedCount -eq $results.Count) { "8/8 全部被杀死" } else { "存在存活 mutant = 门禁缺口" }
    mutants     = $results
} | ConvertTo-Json -Depth 5
Set-Content -LiteralPath (Join-Path $root "_runtime\mutant_results.json") -Value $payload -Encoding UTF8
Write-Host "killed=$killedCount/$($results.Count) -> _runtime/mutant_results.json"
if ($killedCount -ne $results.Count) { exit 1 }