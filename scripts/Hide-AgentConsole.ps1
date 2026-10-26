param([switch]$Restore,[switch]$ListOnly)
$ErrorActionPreference='Stop'
$shellPath=Join-Path $env:USERPROFILE '.cache\codex-runtimes\codex-primary-runtime\dependencies\native\powershell\pwsh.exe'
if(-not ('AgentConsoleWindowV2' -as [type])){
 Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Text;
public static class AgentConsoleWindowV2 {
 public delegate bool Callback(IntPtr handle, IntPtr parameter);
 [DllImport("user32.dll")] public static extern bool EnumWindows(Callback callback, IntPtr parameter);
 [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr handle, out uint processId);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr handle, StringBuilder text, int length);
 [DllImport("user32.dll")] public static extern bool ShowWindowAsync(IntPtr handle, int command);
 [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr handle);
}
'@
}
$windows=[Collections.Generic.List[object]]::new()
$callback=[AgentConsoleWindowV2+Callback]{param($handle,$unused)
 $ownerId=[uint32]0
 [void][AgentConsoleWindowV2]::GetWindowThreadProcessId($handle,[ref]$ownerId)
 $process=Get-Process -Id $ownerId -ErrorAction SilentlyContinue
 if($process -and $process.ProcessName -eq 'WindowsTerminal'){
  $title=[Text.StringBuilder]::new(2048)
  [void][AgentConsoleWindowV2]::GetWindowText($handle,$title,$title.Capacity)
  if($title.ToString() -eq $shellPath){$windows.Add(@{handle=$handle;pid=$ownerId})}
 }
 return $true
}
[void][AgentConsoleWindowV2]::EnumWindows($callback,[IntPtr]::Zero)
$results=@()
foreach($window in $windows){
 $handle=$window.handle
 $before=[AgentConsoleWindowV2]::IsWindowVisible($handle)
 if(-not $ListOnly){[void][AgentConsoleWindowV2]::ShowWindowAsync($handle,$(if($Restore){5}else{0}))}
 Start-Sleep -Milliseconds 100
 $results+=@{pid=$window.pid;was_visible=$before;visible=[AgentConsoleWindowV2]::IsWindowVisible($handle);process_stopped=$false}
}
ConvertTo-Json -InputObject $results -Compress
