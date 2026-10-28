param([string]$Project='E:\freetime\AI_Sing\yue2-261011\yue2-261011\app',[switch]$WithoutWatchdog)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$localConfig=Join-Path $workspace 'secrets\local-deployment.json'
if(-not $PSBoundParameters.ContainsKey('Project') -and (Test-Path -LiteralPath $localConfig -PathType Leaf)){
 $local=Get-Content -LiteralPath $localConfig -Raw -Encoding UTF8 | ConvertFrom-Json
 if($local.backend -eq 'wsl' -and $local.music_service){
  if([string]$local.music_service -notmatch '^wechat-agent-music(?:-[a-f0-9]+)?\.service$'){throw 'Invalid managed music service name.'}
  & (Join-Path $PSScriptRoot 'Invoke-HiddenNative.ps1') -FilePath (Get-Command wsl.exe).Source -Arguments @('-d',[string]$local.distro,'-u',[string]$local.linux_user,'--exec','systemctl','--user','start',[string]$local.music_service)
  if($LASTEXITCODE -ne 0){throw 'Managed local music service failed to start; inspect its service log.'}
  Write-Output '本机WSL受管音乐服务已请求启动；通过管理端检查就绪状态。'
  exit 0
 }
}
$state=Join-Path $workspace 'secrets\yue-jobs'
$token=Join-Path $workspace 'secrets\yue-bridge-token.txt'
if(-not(Test-Path -LiteralPath $token)){throw '请先由管理端部署本地YuE桥接令牌。'}
$stopFile=Join-Path $workspace 'secrets\yue-watchdog.stop'
if(Test-Path -LiteralPath $stopFile){Remove-Item -LiteralPath $stopFile}
New-Item -ItemType Directory -Force -Path (Join-Path $workspace 'temp') | Out-Null
$bind=(Get-NetIPAddress -AddressFamily IPv4 | Where-Object InterfaceAlias -Like '*WSL*' | Select-Object -First 1).IPAddress
if(-not $bind){$bind='127.0.0.1'}
if(-not(Get-NetTCPConnection -State Listen -LocalPort 8188 -ErrorAction SilentlyContinue)){
 if(-not(Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'python.exe' -and $_.CommandLine -like ('*'+(Join-Path $Project 'comfy\ComfyUI\main.py')+'*') })){
 $python=Join-Path $Project 'comfy\python_embeded\python.exe'
 $main=Join-Path $Project 'comfy\ComfyUI\main.py'
 $output=Join-Path $workspace 'temp\yue-agent-output'
 New-Item -ItemType Directory -Force -Path $output | Out-Null
 & (Join-Path $PSScriptRoot 'Start-HiddenProcess.ps1') -FilePath $python -Arguments @('-s',$main,'--listen','127.0.0.1','--port','8188','--lowvram','--disable-auto-launch','--output-directory',$output) -WorkingDirectory (Split-Path $main -Parent) -StandardOutput (Join-Path $workspace 'temp\yue-comfy.stdout.log') -StandardError (Join-Path $workspace 'temp\yue-comfy.stderr.log') | Out-Null
 }
}
if(-not(Get-NetTCPConnection -State Listen -LocalPort 6188 -ErrorAction SilentlyContinue)){
 if(-not(Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'python.exe' -and $_.CommandLine -like ('*'+(Join-Path $PSScriptRoot 'yue_bridge.py')+'*') })){
 $workflowDir=Join-Path $workspace 'components\yue2\app\workflows_api'
 $bridgeArguments=@((Join-Path $PSScriptRoot 'yue_bridge.py'),'--project',$Project,'--workflow-dir',$workflowDir,'--state',$state,'--token-file',$token,'--bind',$bind)
 $singingConfig=Join-Path $workspace 'secrets\singing-voice\config.json'
 if(Test-Path -LiteralPath $singingConfig){$bridgeArguments+=@('--singing-voice-config',$singingConfig)}
 & (Join-Path $PSScriptRoot 'Start-HiddenProcess.ps1') -FilePath (Get-Command python).Source -Arguments $bridgeArguments -StandardOutput (Join-Path $workspace 'temp\yue-bridge.stdout.log') -StandardError (Join-Path $workspace 'temp\yue-bridge.stderr.log') | Out-Null
 }
}
if(-not $WithoutWatchdog){
 $scheduled=Get-ScheduledTask -TaskName 'WechatAgent-YuE-*' -ErrorAction SilentlyContinue | Where-Object { $_.Actions.Arguments -like ('*'+(Join-Path $PSScriptRoot 'hidden_process.pyw')+'*') -and $_.Actions.Arguments -like '*--watch-yue*' }
 if($scheduled){$scheduled | Start-ScheduledTask}
 else {
 $watch=Join-Path $PSScriptRoot 'Watch-Local-YuE.ps1'
 if(-not(Get-CimInstance Win32_Process | Where-Object { $_.Name -match '^(pwsh|powershell)\.exe$' -and $_.CommandLine -like ('*'+$watch+'*') })){
  & (Join-Path $PSScriptRoot 'Start-HiddenProcess.ps1') -FilePath (Get-Process -Id $PID).Path -Arguments @('-NoProfile','-NonInteractive','-File',$watch,'-Project',$Project) | Out-Null
 }
 }
}
Write-Output ('本地YuE桥接启动中： http://'+$bind+':6188；原有服务不会重复启动。')
