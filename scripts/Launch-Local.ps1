param(
 [ValidateSet('Start','Web','Music','Status')][string]$Action='Start',
 [switch]$NoBrowser,
 [switch]$NoDialog,
 [switch]$CheckOnly
)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$url='http://127.0.0.1:8081/'
$logDir=Join-Path $workspace 'temp\launcher'
$utf8=[Text.UTF8Encoding]::new($false)
function Test-Web {
 try { $result=Invoke-RestMethod -Uri ($url+'healthz') -TimeoutSec 3; return $result.ok -eq $true } catch {return $false}
}
function Show-Result([string]$Text,[bool]$Failed=$false) {
 Write-Output $Text
 if(-not $NoDialog){
  Add-Type -AssemblyName System.Windows.Forms
  $icon=if($Failed){[Windows.Forms.MessageBoxIcon]::Error}else{[Windows.Forms.MessageBoxIcon]::Information}
  [Windows.Forms.MessageBox]::Show($Text,'Blue Whale Agent',[Windows.Forms.MessageBoxButtons]::OK,$icon) | Out-Null
 }
}
function Invoke-LauncherScript([string]$Name) {
 $info=[Diagnostics.ProcessStartInfo]::new()
 $info.FileName=(Get-Process -Id $PID).Path
 $info.Arguments='-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "'+(Join-Path $PSScriptRoot $Name)+'"'
 $info.WorkingDirectory=$workspace
 $info.UseShellExecute=$false
 $info.CreateNoWindow=$true
 $info.RedirectStandardOutput=$true
 $info.RedirectStandardError=$true
 $info.StandardOutputEncoding=$utf8
 $info.StandardErrorEncoding=$utf8
 $process=[Diagnostics.Process]::new()
 $process.StartInfo=$info
 try {
  [void]$process.Start()
  $stdout=$process.StandardOutput.ReadToEndAsync()
  $stderr=$process.StandardError.ReadToEndAsync()
  $process.WaitForExit()
  [IO.File]::AppendAllText($log,$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult(),$utf8)
  if($process.ExitCode -ne 0){throw ($Name+' failed with exit code '+$process.ExitCode+'. Inspect the launcher log.')}
 } finally {$process.Dispose()}
}
try {
 if(-not(Test-Path -LiteralPath (Join-Path $workspace 'secrets\local-deployment.json') -PathType Leaf) -and (Test-Path -LiteralPath (Join-Path $workspace 'secrets\docker-deployment.json') -PathType Leaf)){
  $dockerArgs=@{Action=$Action;NoBrowser=[bool]$NoBrowser;NoDialog=[bool]$NoDialog;CheckOnly=[bool]$CheckOnly}
  & (Join-Path $PSScriptRoot 'Start-Docker.ps1') @dockerArgs
  exit $LASTEXITCODE
 }
 foreach($name in @('Start-Local-Admin.ps1','Start-Local-YuE.ps1')){
  if(-not(Test-Path -LiteralPath (Join-Path $PSScriptRoot $name) -PathType Leaf)){throw ('Missing launcher dependency: '+$name)}
 }
 if($CheckOnly){
  @{action=$Action;workspace=$workspace;admin_url=$url;wsl_available=[bool](Get-Command wsl.exe -ErrorAction SilentlyContinue);dependencies_valid=$true} | ConvertTo-Json -Compress
  exit 0
 }
 New-Item -ItemType Directory -Path $logDir -Force | Out-Null
 $log=Join-Path $logDir ('launch-'+(Get-Date -Format 'yyyyMMdd-HHmmss-fff')+'-'+$PID+'.log')
 [IO.File]::WriteAllText($log,('Action: '+$Action+[Environment]::NewLine),$utf8)
 switch($Action){
  'Start' {
   Invoke-LauncherScript 'Start-Local-Admin.ps1'
   if(-not(Test-Web)){throw 'The management page is not ready. Inspect the launcher log; existing login data was preserved.'}
   if(-not $NoBrowser){Start-Process $url | Out-Null}
   Write-Output ('Agent services are ready. '+$url)
  }
  'Web' {
   if(-not(Test-Web)){throw 'The management service is offline. Double-click the launcher without arguments to start it.'}
   if(-not $NoBrowser){Start-Process $url | Out-Null}
   Write-Output ('Management page: '+$url)
  }
  'Music' {
   Invoke-LauncherScript 'Start-Local-YuE.ps1'
   Show-Result ('YuE startup requested. Initialization can take time; check the provider page for readiness.'+[Environment]::NewLine+'Log: '+$log)
  }
  'Status' {
   $web=if(Test-Web){'ready'}else{'offline'}
   $ports=@(Get-NetTCPConnection -State Listen -LocalPort 6188,8188 -ErrorAction SilentlyContinue | Select-Object -ExpandProperty LocalPort)
   Show-Result ('Management: '+$web+[Environment]::NewLine+'YuE ports listening: '+($ports -join ', ')+[Environment]::NewLine+$url)
  }
 }
 [IO.File]::AppendAllText($log,('Completed: '+$Action+[Environment]::NewLine),$utf8)
 exit 0
} catch {
 $message=$_.Exception.Message
 if($log){[IO.File]::AppendAllText($log,('Error: '+$message+[Environment]::NewLine),$utf8);$message+=[Environment]::NewLine+'Log: '+$log}
 Show-Result $message $true
 exit 1
}
