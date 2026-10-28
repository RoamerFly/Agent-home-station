param([string]$Distro='Ubuntu-New',[string]$LinuxUser='fly',[Parameter(Mandatory=$true)][string]$Marker)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$taskName='WechatAgent-WSL-'+$Marker.Replace('wechat-agent-anchor-','')
$pythonw=(Get-Command pythonw.exe -ErrorAction Stop).Source
$launcher=Join-Path $PSScriptRoot 'hidden_process.pyw'
$task=Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
$arguments='"'+$launcher+'" --keep-wsl --workspace "'+$workspace+'" --distro "'+$Distro+'" --linux-user "'+$LinuxUser+'" --marker "'+$Marker+'"'
if(-not $task -or $task.Actions.Execute -ne $pythonw -or $task.Actions.Arguments -ne $arguments){
 $user=[Security.Principal.WindowsIdentity]::GetCurrent().Name
 $action=New-ScheduledTaskAction -Execute $pythonw -Argument $arguments -WorkingDirectory $workspace
 $principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
 $settings=New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
 # Manual-only: separate lifecycle from a temporary Codex/launcher process.
 $definition=New-ScheduledTask -Action $action -Principal $principal -Settings $settings -Description 'Manual-only console-free WSL session anchor. No boot/logon triggers; preserves existing WeChat services.'
 Register-ScheduledTask -TaskName $taskName -InputObject $definition -Force | Out-Null
}
Start-ScheduledTask -TaskName $taskName
@{task=$taskName;marker=$Marker;automatic_start=$false}
