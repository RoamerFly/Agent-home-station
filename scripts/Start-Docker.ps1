param(
 [ValidateSet('Start','Web','Music','Status','Update')][string]$Action='Start',
 [string]$ImageFile='',
 [switch]$NoBrowser,
 [switch]$NoDialog,
 [switch]$CheckOnly
)
$ErrorActionPreference='Stop'
$taskRoot=Split-Path $PSScriptRoot -Parent
$compose=Join-Path $taskRoot 'deploy\docker\compose.agent.yaml'
$marker=Join-Path $taskRoot 'secrets\docker-deployment.json'
$url='http://127.0.0.1:8081'
$utf8=[Text.UTF8Encoding]::new($false)
$log=$null
function Show-Result([string]$Text,[bool]$Failed=$false) {
 Write-Output $Text
 if(-not $NoDialog){
  Add-Type -AssemblyName System.Windows.Forms
  $icon=if($Failed){[Windows.Forms.MessageBoxIcon]::Error}else{[Windows.Forms.MessageBoxIcon]::Information}
  [Windows.Forms.MessageBox]::Show($Text,'Blue Whale Docker',[Windows.Forms.MessageBoxButtons]::OK,$icon) | Out-Null
 }
}
function Invoke-Docker([string[]]$Arguments,[switch]$PrivateOutput) {
 $info=[Diagnostics.ProcessStartInfo]::new()
 $info.FileName=(Get-Command docker.exe -ErrorAction Stop).Source
 # ArgumentList is unavailable in Windows PowerShell 5.1.
 $quoted=foreach($arg in $Arguments){
  if($arg.Contains('"') -or $arg.Contains("`r") -or $arg.Contains("`n")){throw 'Invalid Docker argument'}
  '"'+($arg -replace '(\\+)$','$1$1')+'"'
 }
 $info.Arguments=$quoted -join ' '
 $info.WorkingDirectory=$taskRoot
 $info.UseShellExecute=$false;$info.CreateNoWindow=$true
 $info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
 $info.StandardOutputEncoding=$utf8;$info.StandardErrorEncoding=$utf8
 $process=[Diagnostics.Process]::new();$process.StartInfo=$info
 try{
  [void]$process.Start();$out=$process.StandardOutput.ReadToEndAsync();$err=$process.StandardError.ReadToEndAsync();$process.WaitForExit()
  $text=$out.GetAwaiter().GetResult();$errorText=$err.GetAwaiter().GetResult()
  if($log -and -not $PrivateOutput){[IO.File]::AppendAllText($log,$text+$errorText,$utf8)}
  if($process.ExitCode -ne 0){throw ('Docker failed ('+$process.ExitCode+'). '+$(if($PrivateOutput){'Private operation failed; inspect the service locally.'}else{$errorText}))}
  return $text.Trim()
 }finally{$process.Dispose()}
}
function Invoke-Compose([string[]]$Arguments){
 $files=@('compose','-f',$compose)
 if($script:musicEnabled){$files+=@('-f',(Join-Path $taskRoot 'deploy\docker\compose.music.yaml'))}
 Invoke-Docker -Arguments ($files+$Arguments)
}
function Wait-Web {
 for($i=0;$i -lt 90;$i++){
  try{if((Invoke-RestMethod ($url+'/healthz') -TimeoutSec 2).ok){return}}catch{}
  Start-Sleep -Seconds 2
 }
 throw 'The Docker management page is not healthy; persistent data was retained.'
}
try{
 if(-not(Test-Path -LiteralPath $compose -PathType Leaf)){throw 'Compose source is missing.'}
 if($CheckOnly){@{action=$Action;backend='docker';workspace=$taskRoot;compose=$compose;admin_url=$url;dependencies_valid=$true} | ConvertTo-Json -Compress;exit 0}
 $config=if(Test-Path -LiteralPath $marker){Get-Content -LiteralPath $marker -Raw -Encoding UTF8 | ConvertFrom-Json}else{$null}
 if($config){
  if($config.image){$env:WECHAT_AGENT_IMAGE=[string]$config.image}
  if($config.account){$env:WECHAT_NATIVE_ACCOUNT=[string]$config.account}
  if($config.data_volume){$env:WECHAT_DATA_VOLUME=[string]$config.data_volume}
  if($config.account_directory){$env:WECHAT_ACCOUNT_DIRECTORY=[string]$config.account_directory}
  if($config.music_image){$env:WECHAT_MUSIC_IMAGE=[string]$config.music_image}
 }
 $script:musicEnabled=$config -and $config.music_enabled -eq $true
 if(-not $env:WECHAT_AGENT_IMAGE){$env:WECHAT_AGENT_IMAGE='wechat-agent:local'}
 $directory=Join-Path $taskRoot 'temp\launcher';[void](New-Item -ItemType Directory -Path $directory -Force)
 $log=Join-Path $directory ('docker-'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'.log')
 try{[void](Invoke-Docker -Arguments @('info','--format','{{.ServerVersion}}'))}catch{[void](Invoke-Docker -Arguments @('desktop','start','--timeout','150'))}
 if($Action -eq 'Status'){
  Show-Result ((Invoke-Compose -Arguments @('ps'))+[Environment]::NewLine+$url);exit 0
 }
 if($Action -eq 'Web'){Wait-Web;if(-not $NoBrowser){Start-Process $url | Out-Null};exit 0}
 if($Action -eq 'Music'){
  if(-not $script:musicEnabled){throw 'The managed Docker music service has not passed installation/validation yet.'}
  [void](Invoke-Compose -Arguments @('up','-d','--pull','never','music'));Show-Result 'Managed music service started.';exit 0
 }
 $ticket=$null;$session=$null;$csrf=$null
 if($Action -eq 'Update'){
  if(-not $ImageFile){
   Add-Type -AssemblyName System.Windows.Forms
   $picker=[Windows.Forms.OpenFileDialog]::new();$picker.Filter='Docker image (*.tar)|*.tar';$picker.Title='Select the new Blue Whale image'
   if($picker.ShowDialog() -ne [Windows.Forms.DialogResult]::OK){exit 0};$ImageFile=$picker.FileName
  }
  $file=Get-Item -LiteralPath $ImageFile -ErrorAction Stop
  $checksum=$file.FullName+'.sha256'
  if(Test-Path -LiteralPath $checksum){
   $expected=(Get-Content -LiteralPath $checksum -Raw).Trim().Split(' ')[0]
   if($expected -notmatch '^[0-9a-fA-F]{64}$' -or (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash -ne $expected){throw 'Image SHA-256 verification failed.'}
  }
  [void](Invoke-Docker -Arguments @('load','--input',$file.FullName))
  [void](Invoke-Docker -Arguments @('image','inspect',$env:WECHAT_AGENT_IMAGE,'--format','{{.Id}}'))
  $container=Invoke-Compose -Arguments @('ps','-q','agent')
  if($container){
   # Drain every account before replacing the executable. Credentials stay in
   # memory and are never appended to the launcher log or the image.
   $password=Invoke-Docker -Arguments @('exec',$container,'sh','-c','cat /data/admin/admin-password 2>/dev/null || cat /data/admin/admin-token') -PrivateOutput
   $login=Invoke-RestMethod ($url+'/api/login') -Method Post -ContentType 'application/json' -Body (@{password=$password}|ConvertTo-Json -Compress) -Headers @{Origin=$url} -SessionVariable session
   $csrf=$login.csrf;$headers=@{Origin=$url;'X-CSRF-Token'=$csrf}
   $ticket=(Invoke-RestMethod ($url+'/api/runtime/drain') -Method Post -ContentType 'application/json' -Body '{}' -WebSession $session -Headers $headers).ticket
   while(-not (Invoke-RestMethod ($url+'/api/runtime/drain?ticket='+[Uri]::EscapeDataString($ticket)) -WebSession $session).ready){Start-Sleep -Seconds 2}
  }
 }
 try{
  [void](Invoke-Compose -Arguments @('up','-d','--pull','never'))
  Wait-Web
 }finally{
  if($ticket){try{[void](Invoke-RestMethod ($url+'/api/runtime/undrain') -Method Post -ContentType 'application/json' -Body (@{ticket=$ticket}|ConvertTo-Json -Compress) -WebSession $session -Headers $headers)}catch{}}
 }
 if(-not $NoBrowser){Start-Process $url | Out-Null}
 Show-Result ('Docker services are ready. '+$url)
}catch{
 Show-Result ($_.Exception.Message+[Environment]::NewLine+'Data retained. Log: '+$log) $true
 exit 1
}
