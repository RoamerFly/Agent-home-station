param([string]$Project='E:\freetime\AI_Sing\yue2-261011\yue2-261011\app')
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$projectFull=[IO.Path]::GetFullPath($Project)
$main=Join-Path $projectFull 'comfy\ComfyUI\main.py'
$bridge=Join-Path $PSScriptRoot 'yue_bridge.py'
$watch=Join-Path $PSScriptRoot 'Watch-Local-YuE.ps1'
if(-not(Test-Path -LiteralPath $main -PathType Leaf)){throw 'YuE project main.py not found; no processes stopped.'}
$listener=Get-NetTCPConnection -State Listen -LocalPort 8188 -ErrorAction SilentlyContinue
if($listener){
 $queue=Invoke-RestMethod -Uri 'http://127.0.0.1:8188/queue' -TimeoutSec 8
 if(@($queue.queue_running).Count -gt 0 -or @($queue.queue_pending).Count -gt 0){throw 'YuE is busy; wait for the existing jobs before stopping.'}
}
$all=Get-CimInstance Win32_Process
$owned=@($all | Where-Object {
 $_.ProcessId -ne $PID -and $_.CommandLine -and (
  ($_.Name -eq 'python.exe' -and ($_.CommandLine.Contains($main) -or $_.CommandLine.Contains($bridge))) -or
  ($_.Name -match '^(pwsh|powershell)\.exe$' -and $_.CommandLine.Contains($watch) -and $_.CommandLine -notmatch '(?i)-Command')
 )
})
foreach($port in 6188,8188){
 foreach($item in @(Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue)){
  if($item.OwningProcess -notin $owned.ProcessId){throw "Port $port belongs to another program; nothing stopped."}
 }
}
$stopFile=Join-Path $workspace 'secrets\yue-watchdog.stop'
[IO.File]::WriteAllText($stopFile,(Get-Date).ToUniversalTime().ToString('o'),[Text.UTF8Encoding]::new($false))
$scheduled=@(Get-ScheduledTask -TaskName 'WechatAgent-YuE-*' -ErrorAction SilentlyContinue | Where-Object { $_.Actions.Arguments -like ('*'+$watch+'*') })
foreach($task in $scheduled){$task | Stop-ScheduledTask}
foreach($process in $owned){
 $current=Get-CimInstance Win32_Process -Filter ('ProcessId='+$process.ProcessId)
 if($current -and $current.CreationDate -eq $process.CreationDate -and $current.CommandLine -eq $process.CommandLine){Stop-Process -Id $process.ProcessId -ErrorAction Stop}
}
Start-Sleep -Milliseconds 500
$remaining=@(Get-NetTCPConnection -State Listen -LocalPort 6188,8188 -ErrorAction SilentlyContinue)
if($remaining.Count){throw 'YuE ports still listening; inspect the actual processes.'}
@{state='stopped';stopped_pids=@($owned.ProcessId);ports_closed=@(6188,8188);watchdog_paused=$true} | ConvertTo-Json -Compress
