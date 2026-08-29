# start-production.ps1 — 生产化：安装/启动/停止/状态（Windows 计划任务服务化）
# 用法:
#   powershell -File scripts/start-production.ps1 install    # 安装开机自启任务
#   powershell -File scripts/start-production.ps1 start      # 立即启动
#   powershell -File scripts/start-production.ps1 stop       # 停止
#   powershell -File scripts/start-production.ps1 status     # 状态
#   powershell -File scripts/start-production.ps1 uninstall  # 卸载任务
$ErrorActionPreference = "Stop"
$taskName = "Notion2API"
$exe = "C:\Users\Administrator\notion2api\notion2api.exe"
$cfg = "C:\Users\Administrator\notion2api\config.json"
$workdir = "C:\Users\Administrator\notion2api"
$logDir = "C:\Users\Administrator\notion2api\logs"
$stdout = Join-Path $logDir "stdout.log"
$stderr = Join-Path $logDir "stderr.log"

if (-not (Test-Path $logDir)) { New-Item -ItemType Directory -Path $logDir -Force | Out-Null }
if (-not (Test-Path $exe)) { Write-Error "notion2api.exe not found at $exe"; exit 1 }

function Ensure-EnvPath {
    $goBin = "C:\Program Files\Go\bin"
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$goBin*") {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$goBin", "User")
    }
}

switch ($args[0]) {
    "install" {
        Ensure-EnvPath
        $action = New-ScheduledTaskAction -Execute $exe -Argument "--config `"$cfg`"" -WorkingDirectory $workdir
        $trigger = New-ScheduledTaskTrigger -AtStartup
        $settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
        Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Description "notion2api production service" -Force | Out-Null
        Write-Output "[ok] task installed: $taskName (boot auto-start, auto-restart x3)"
    }
    "start" {
        Ensure-EnvPath
        Get-Process notion2api -ErrorAction SilentlyContinue | Stop-Process -Force
        Start-Sleep 1
        Start-Process -FilePath $exe -ArgumentList "--config","`"$cfg`"" -WorkingDirectory $workdir -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr
        Start-Sleep 3
        $hz = Invoke-RestMethod -Uri "http://127.0.0.1:8787/healthz" -TimeoutSec 10
        Write-Output "[ok] started, health=$($hz.ok) models=$($hz.model_count) session_ready=$($hz.session_ready)"
        Write-Output "[log] stdout=$stdout"
    }
    "stop" {
        Get-Process notion2api -ErrorAction SilentlyContinue | Stop-Process -Force
        Write-Output "[ok] stopped"
    }
    "status" {
        $proc = Get-Process notion2api -ErrorAction SilentlyContinue
        if ($proc) {
            Write-Output "running pid=$($proc.Id)"
            try { $hz = Invoke-RestMethod -Uri "http://127.0.0.1:8787/healthz" -TimeoutSec 5; Write-Output "health=$($hz.ok) models=$($hz.model_count) session_ready=$($hz.session_ready)" } catch { Write-Output "health: unreachable" }
        } else {
            Write-Output "not running"
        }
        $task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Write-Output "task: $($task.State)"
    }
    "uninstall" {
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
        Write-Output "[ok] task removed"
    }
    default {
        Write-Output "usage: start-production.ps1 [install|start|stop|status|uninstall]"
    }
}