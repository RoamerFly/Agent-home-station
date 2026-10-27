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
$pythonw=(Get-Command pythonw.exe -ErrorAction Stop).Source
$launcher=Join-Path $PSScriptRoot 'hidden_process.pyw'
$action=New-ScheduledTaskAction -Execute $pythonw -Argument ('"'+$launcher+'" --watch-yue --workspace "'+$workspace+'" --project "'+$Project+'" --shell "'+$shell+'"') -WorkingDirectory $workspace
$principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
# Replacing a task action does not stop its existing shell. Stop only this
# workspace's watchdog, never its backend children or unrelated PowerShell.
$oldTask=Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if($oldTask){$oldTask | Stop-ScheduledTask}
$oldWatch=@(Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.Name -match '^(pwsh|powershell)\.exe$' -and $_.CommandLine -and $_.CommandLine.Contains($watch) -and $_.CommandLine -notmatch '(?i)-Command' })
foreach($item in $oldWatch){
 $current=Get-CimInstance Win32_Process -Filter ('ProcessId='+$item.ProcessId)
 if($current -and $current.CreationDate -eq $item.CreationDate -and $current.CommandLine -eq $item.CommandLine){Stop-Process -Id $item.ProcessId -ErrorAction Stop}
}
$task=New-ScheduledTask -Action $action -Principal $principal -Settings $settings -Description 'Manual-only, console-free YuE watchdog. No logon/boot trigger and no automatic generation resubmission.'
Register-ScheduledTask -TaskName $taskName -InputObject $task -Force | Out-Null
Start-ScheduledTask -TaskName $taskName
Write-Output ('已注册并启动无控制台YuE守护任务：'+$taskName+'；仅手动启动，不随开机或登录启动。')
