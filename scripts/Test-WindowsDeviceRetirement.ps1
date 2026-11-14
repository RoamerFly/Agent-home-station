param(
 [string]$SourceDirectory,
 [ValidateSet('Management','Station')][string]$Target='Management',
 [switch]$Test
)
$ErrorActionPreference='Stop'
if(-not $SourceDirectory){$SourceDirectory=Split-Path $PSScriptRoot -Parent}
$SourceDirectory=[IO.Path]::GetFullPath($SourceDirectory)
$package=if($Target -eq 'Station'){'./cmd/agent-home-station'}else{'./cmd/wechat-admin'}
$oldOS=$env:GOOS; $oldArch=$env:GOARCH
Push-Location $SourceDirectory
try {
 $env:GOOS='windows'; $env:GOARCH='amd64'
 # Avoid nested quotes in the Go template: Windows PowerShell 5's native
 # argument marshalling otherwise removes the separator quotes.
 $graph=@(& go list -deps -f '{{.ImportPath}}|{{.Dir}}|{{.GoFiles}}|{{.Imports}}|{{.CgoFiles}}' $package)
 if($LASTEXITCODE -ne 0){throw 'Windows device retirement dependency audit failed.'}
 $drivers=@('wechat-bot/internal/adbserver','wechat-bot/internal/devicebroker')
 $stationForbidden=$drivers+@('wechat-bot/internal/admin','wechat-bot/internal/devicespec')
 $adminBridgeFiles=@('mobile_device_plugin.go','mobile_observation.go','mobile_workflow_plugin.go')
 foreach($line in $graph){
  $parts=$line.Split('|')
  if($parts.Count -ne 5){throw 'Windows device retirement dependency metadata is invalid.'}
  $name=$parts[0]; $directory=$parts[1]
  $files=@($parts[2].Trim([char[]]'[]').Split(' ',[StringSplitOptions]::RemoveEmptyEntries))+
    @($parts[4].Trim([char[]]'[]').Split(' ',[StringSplitOptions]::RemoveEmptyEntries))
  $imports=$parts[3].Trim([char[]]'[]').Split(' ',[StringSplitOptions]::RemoveEmptyEntries)
  if($Target -eq 'Station' -and $stationForbidden -contains $name){throw 'LocalStation imports a retired management or device runtime package.'}
  foreach($driver in $drivers){
   if($imports -notcontains $driver){continue}
   # The shared legacy adapter still compiles its authorized observation
   # bridge. Only the three reviewed host files may bind that implementation;
   # commands and the standalone station must never bind a native driver.
   $allowed=($name -eq 'wechat-bot/internal/devicebroker' -and $driver -eq 'wechat-bot/internal/adbserver') -or
     ($name -eq 'wechat-bot/internal/admin' -and $driver -eq 'wechat-bot/internal/devicebroker')
   if(-not $allowed){throw ('Unreviewed Windows device driver binding: '+$name+' -> '+$driver)}
  }
  if($name -eq 'wechat-bot/internal/admin'){
   foreach($file in $files){
    if(-not $file){continue}
    $content=[IO.File]::ReadAllText((Join-Path $directory $file))
    if($content.Contains('"wechat-bot/internal/devicebroker"') -and $adminBridgeFiles -notcontains $file){throw ('Unreviewed Windows admin device bridge source: '+$file)}
    if($content.Contains('"wechat-bot/internal/adbserver"')){throw ('Windows admin directly binds an ADB server: '+$file)}
   }
  }
 }
 if($Test){
  if($Target -eq 'Station'){
   & go test ./cmd/agent-home-station -run '^TestStationBuildExcludesLegacyManagementAndDevices$' -count=1
   if($LASTEXITCODE -ne 0){throw 'LocalStation device exclusion regression failed.'}
  }else{
   # These checks use synthetic stores only; no device, account or native
   # runtime is started. Dependency names alone cannot prove retirement once
   # observation and retired adapters share the same Go package.
   & go test ./internal/admin -run '^TestRetiredDevice(Control|Archive)' -count=1
   if($LASTEXITCODE -ne 0){throw 'Windows retired device runtime regression failed.'}
   $entryTests=@(& go test ./cmd/wechat-admin -list '^TestWindowsManagerRetiresDeviceControl$')
   if($LASTEXITCODE -ne 0 -or $entryTests -notcontains 'TestWindowsManagerRetiresDeviceControl'){throw 'Windows manager retirement entrypoint test is missing or cannot compile.'}
   & go test ./cmd/wechat-admin -run '^TestWindowsManagerRetiresDeviceControl$' -count=1
   if($LASTEXITCODE -ne 0){throw 'Windows manager retirement entrypoint regression failed.'}
  }
 }
 Write-Output ('Windows device retirement boundary verified: '+$Target)
} finally {$env:GOOS=$oldOS; $env:GOARCH=$oldArch; Pop-Location}
