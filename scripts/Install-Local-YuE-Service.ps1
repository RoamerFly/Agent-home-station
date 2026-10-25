param([string]$Project='E:\freetime\AI_Sing\yue2-261011\yue2-261011\app')
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$watch=Join-Path $PSScriptRoot 'Watch-Local-YuE.ps1'
$sha=[Security.Cryptography.SHA256]::Create()
$identity=([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($workspace.ToLowerInvariant())))).Replace('-','').Substring(0,12)
$sha.Dispose()
$taskName='WechatAgent-YuE-'+$identity
$user=[Security.Principal.WindowsIdentity]::GetCurrent().Name
$shell=(Get-Process -Id $PID).Path
if($PSVersionTable.PSVersion.Major -lt 7){throw '请使用PowerShell 7运行，以确保脚本UTF-8解析一致。'}
$action=New-ScheduledTaskAction -Execute $shell -Argument ('-NoProfile -NonInteractive -WindowStyle Hidden -File "'+$watch+'" -Project "'+$Project+'"') -WorkingDirectory $workspace
$trigger=New-ScheduledTaskTrigger -AtLogOn -User $user
$principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Description 'Keep this workspace local YuE bridge and ComfyUI available; no automatic generation resubmission.' -Force | Out-Null
Start-ScheduledTask -TaskName $taskName
Write-Output ('已注册并启动当前用户的YuE守护任务：'+$taskName+'；登录时启动，不保存用户密码，不请求管理员权限。')
