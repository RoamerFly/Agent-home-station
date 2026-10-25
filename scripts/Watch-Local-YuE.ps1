param([string]$Project='E:\freetime\AI_Sing\yue2-261011\yue2-261011\app')
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$sha=[Security.Cryptography.SHA256]::Create()
$identity=([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($workspace.ToLowerInvariant())))).Replace('-','').Substring(0,20)
$sha.Dispose()
$mutex=[Threading.Mutex]::new($false,('Local\WechatYuE-'+$identity))
$owned=$false
try {$owned=$mutex.WaitOne(0)} catch [Threading.AbandonedMutexException] {$owned=$true}
if(-not $owned){$mutex.Dispose();exit}
$status=Join-Path $workspace 'temp\yue-watchdog.json'
$stopFile=Join-Path $workspace 'secrets\yue-watchdog.stop'
$attempts=0
try{
 while(-not(Test-Path -LiteralPath $stopFile)){
  $ports=@(Get-NetTCPConnection -State Listen -LocalPort 6188,8188 -ErrorAction SilentlyContinue | Select-Object -ExpandProperty LocalPort)
  $missing=@(6188,8188 | Where-Object {$_ -notin $ports})
  $result='listening'
  if($missing.Count){
   $attempts++
   try { & (Join-Path $PSScriptRoot 'Start-Local-YuE.ps1') -Project $Project -WithoutWatchdog | Out-Null; $result='starting' }
   catch { $result='start_failed' }
  }
  $json=@{time=(Get-Date).ToUniversalTime().ToString('o');pid=$PID;state=$result;restart_checks=$attempts;missing_ports=$missing} | ConvertTo-Json -Compress
  [IO.File]::WriteAllText($status,$json,[Text.UTF8Encoding]::new($false))
  Start-Sleep -Seconds $(if($result -eq 'listening'){10}else{30})
 }
} finally {$mutex.ReleaseMutex();$mutex.Dispose()}
