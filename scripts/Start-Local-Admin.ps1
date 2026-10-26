param(
 [string]$Distro='Ubuntu-New',
 [string]$LinuxUser='fly',
 [string]$DataRoot='/home/fly/.local/share/wechat-bot-native-research-20261024',
 [string]$Account='background-test-20261024'
)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
$digest=[Security.Cryptography.SHA256]::Create()
try {$identity=([BitConverter]::ToString($digest.ComputeHash([Text.Encoding]::UTF8.GetBytes($workspace.ToLowerInvariant())))).Replace('-','').Substring(0,16)} finally {$digest.Dispose()}
$marker='wechat-agent-anchor-'+$identity
# A foreground WSL session keeps this local development instance and its user
# service manager alive. Do not change distro-wide idle settings or install a
# Windows boot task. Linux server deployments use their normal service manager.
$existing=@(Get-CimInstance Win32_Process | Where-Object {$_.Name -eq 'wsl.exe' -and $_.CommandLine -like ('*'+$marker+'*')})
if(-not $existing.Count){
 $anchor=Start-Process -FilePath 'wsl.exe' -ArgumentList @('-d',$Distro,'-u',$LinuxUser,'--','/bin/bash','-c',('"exec -a '+$marker+' sleep infinity"')) -WindowStyle Hidden -PassThru
 New-Item -ItemType Directory -Path (Join-Path $workspace 'temp') -Force | Out-Null
 @{pid=$anchor.Id;marker=$marker;distro=$Distro;started=(Get-Date).ToUniversalTime().ToString('o')} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $workspace 'temp/local-admin-anchor.json') -Encoding utf8
}
& wsl.exe -d $Distro -u $LinuxUser -- systemctl --user start wechat-bot-native.service wechat-bot-web.service
if($LASTEXITCODE -ne 0){throw 'WSL user services did not start; existing profile was not changed.'}
$state=& wsl.exe -d $Distro -u $LinuxUser -- "$DataRoot/bin/wx" --data-dir $DataRoot --token-file "$DataRoot/api-token" --account $Account --json auth status
if($LASTEXITCODE -ne 0){throw 'Native status unavailable; check the local services.'}
$current=$state | ConvertFrom-Json
if(-not $current.authenticated){
 & wsl.exe -d $Distro -u $LinuxUser -- "$DataRoot/bin/wx" --data-dir $DataRoot --token-file "$DataRoot/api-token" --account $Account --json account connect --detach | Out-Null
 if($LASTEXITCODE -ne 0){throw 'Saved connection could not be started; use the login page without deleting the profile.'}
}
Write-Output 'Local WSL session retained; management page http://127.0.0.1:8081/ . This starts services without sending a chat message.'
