param([string]$Project='E:\freetime\AI_Sing\yue2-261011\yue2-261011\app')
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$state=Join-Path $workspace 'secrets\yue-jobs'
$token=Join-Path $workspace 'secrets\yue-bridge-token.txt'
if(-not(Test-Path -LiteralPath $token)){throw '请先由管理端部署本地YuE桥接令牌。'}
$bind=(Get-NetIPAddress -AddressFamily IPv4 | Where-Object InterfaceAlias -Like '*WSL*' | Select-Object -First 1).IPAddress
if(-not $bind){$bind='127.0.0.1'}
if(-not(Get-NetTCPConnection -State Listen -LocalPort 8188 -ErrorAction SilentlyContinue)){
 $python=Join-Path $Project 'comfy\python_embeded\python.exe'
 $main=Join-Path $Project 'comfy\ComfyUI\main.py'
 $output=Join-Path $workspace 'temp\yue-agent-output'
 New-Item -ItemType Directory -Force -Path $output | Out-Null
 Start-Process -FilePath $python -ArgumentList @('-s',('"'+$main+'"'),'--listen','127.0.0.1','--port','8188','--lowvram','--disable-auto-launch','--output-directory',('"'+$output+'"')) -WorkingDirectory (Split-Path $main -Parent) -WindowStyle Hidden -RedirectStandardOutput (Join-Path $workspace 'temp\yue-comfy.stdout.log') -RedirectStandardError (Join-Path $workspace 'temp\yue-comfy.stderr.log') | Out-Null
}
if(-not(Get-NetTCPConnection -State Listen -LocalPort 6188 -ErrorAction SilentlyContinue)){
 Start-Process -FilePath (Get-Command python).Source -ArgumentList @(('"'+(Join-Path $PSScriptRoot 'yue_bridge.py')+'"'),'--project',('"'+$Project+'"'),'--state',('"'+$state+'"'),'--token-file',('"'+$token+'"'),'--bind',$bind) -WindowStyle Hidden -RedirectStandardOutput (Join-Path $workspace 'temp\yue-bridge.stdout.log') -RedirectStandardError (Join-Path $workspace 'temp\yue-bridge.stderr.log') | Out-Null
}
Write-Output ('本地YuE桥接启动中： http://'+$bind+':6188；原有服务不会重复启动。')
