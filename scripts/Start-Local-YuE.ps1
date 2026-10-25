param([string]$Project='E:\freetime\AI_Sing\yue2-261011\yue2-261011\app',[switch]$WithoutWatchdog)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$state=Join-Path $workspace 'secrets\yue-jobs'
$token=Join-Path $workspace 'secrets\yue-bridge-token.txt'
if(-not(Test-Path -LiteralPath $token)){throw '请先由管理端部署本地YuE桥接令牌。'}
New-Item -ItemType Directory -Force -Path (Join-Path $workspace 'temp') | Out-Null
$bind=(Get-NetIPAddress -AddressFamily IPv4 | Where-Object InterfaceAlias -Like '*WSL*' | Select-Object -First 1).IPAddress
if(-not $bind){$bind='127.0.0.1'}
if(-not(Get-NetTCPConnection -State Listen -LocalPort 8188 -ErrorAction SilentlyContinue)){
 if(-not(Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'python.exe' -and $_.CommandLine -like ('*'+(Join-Path $Project 'comfy\ComfyUI\main.py')+'*') })){
 $python=Join-Path $Project 'comfy\python_embeded\python.exe'
 $main=Join-Path $Project 'comfy\ComfyUI\main.py'
 $output=Join-Path $workspace 'temp\yue-agent-output'
 New-Item -ItemType Directory -Force -Path $output | Out-Null
 Start-Process -FilePath $python -ArgumentList @('-s',('"'+$main+'"'),'--listen','127.0.0.1','--port','8188','--lowvram','--disable-auto-launch','--output-directory',('"'+$output+'"')) -WorkingDirectory (Split-Path $main -Parent) -WindowStyle Hidden -RedirectStandardOutput (Join-Path $workspace 'temp\yue-comfy.stdout.log') -RedirectStandardError (Join-Path $workspace 'temp\yue-comfy.stderr.log') | Out-Null
 }
}
if(-not(Get-NetTCPConnection -State Listen -LocalPort 6188 -ErrorAction SilentlyContinue)){
 if(-not(Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'python.exe' -and $_.CommandLine -like ('*'+(Join-Path $PSScriptRoot 'yue_bridge.py')+'*') })){
 $workflowDir=Join-Path $workspace 'components\yue2\app\workflows_api'
 Start-Process -FilePath (Get-Command python).Source -ArgumentList @(('"'+(Join-Path $PSScriptRoot 'yue_bridge.py')+'"'),'--project',('"'+$Project+'"'),'--workflow-dir',('"'+$workflowDir+'"'),'--state',('"'+$state+'"'),'--token-file',('"'+$token+'"'),'--bind',$bind) -WindowStyle Hidden -RedirectStandardOutput (Join-Path $workspace 'temp\yue-bridge.stdout.log') -RedirectStandardError (Join-Path $workspace 'temp\yue-bridge.stderr.log') | Out-Null
 }
}
if(-not $WithoutWatchdog){
 $scheduled=Get-ScheduledTask -TaskName 'WechatAgent-YuE-*' -ErrorAction SilentlyContinue | Where-Object { $_.Actions.Arguments -like ('*'+(Join-Path $PSScriptRoot 'Watch-Local-YuE.ps1')+'*') }
 if($scheduled){$scheduled | Start-ScheduledTask}
 else {
 $watch=Join-Path $PSScriptRoot 'Watch-Local-YuE.ps1'
 if(-not(Get-CimInstance Win32_Process | Where-Object { $_.Name -match '^(pwsh|powershell)\.exe$' -and $_.CommandLine -like ('*'+$watch+'*') })){
  Start-Process -FilePath (Get-Process -Id $PID).Path -ArgumentList @('-NoProfile','-File',('"'+$watch+'"'),'-Project',('"'+$Project+'"')) -WindowStyle Hidden | Out-Null
 }
 }
}
Write-Output ('本地YuE桥接启动中： http://'+$bind+':6188；原有服务不会重复启动。')
