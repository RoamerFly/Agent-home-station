param([string]$OutputDirectory, [switch]$Test, [switch]$Portable, [switch]$LocalStation, [string]$NativeSourceDirectory, [string]$MediaDirectory, [string]$GoSourcePatch, [string]$DocumentPythonPackagesDirectory, [string]$DocumentTesseractDirectory, [string]$DocumentFontsDirectory)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
# Keep project-local SDK/model caches outside `go test ./...` discovery.
# Nested build snapshots retain their own go.mod; never replace an existing one.
foreach($artifactName in @('temp','dist')){
 $artifactDirectory=Join-Path $workspace $artifactName
 New-Item -ItemType Directory -Force -Path $artifactDirectory | Out-Null
 $boundary=Join-Path $artifactDirectory 'go.mod'
 if(-not(Test-Path -LiteralPath $boundary)){
  [IO.File]::WriteAllText($boundary,("module local.invalid/wechat-agent-"+$artifactName+"`n`ngo 1.25.3`n"),[Text.Encoding]::ASCII)
 }
}
if(-not $OutputDirectory){$OutputDirectory=Join-Path $workspace ($(if($LocalStation){'dist\local-station-preview'}else{'dist\windows-desktop'}))}
$OutputDirectory=[IO.Path]::GetFullPath($OutputDirectory)
$version='1.0.3537.50'
$expectedHash='5EA526BBD728ADDA0DA4D31219267E96460494A427E4894C4E09D9F320F4B9AA'
$cache=Join-Path $workspace ('temp\desktop-deps\webview2-'+$version)
$archive=$cache+'.zip'
New-Item -ItemType Directory -Force -Path (Split-Path $cache -Parent),$OutputDirectory | Out-Null
# The zero-application preview deliberately has no optional tool catalog.  The
# regular desktop package retains the catalog validation/staging contract.
if(-not $LocalStation){
 & python -B (Join-Path $PSScriptRoot 'stage_plugin_catalog.py') --source (Join-Path $workspace 'plugins') --output (Join-Path $OutputDirectory 'plugins')
 if($LASTEXITCODE -ne 0){throw 'Official plugin catalog staging failed; use a fresh output directory.'}
}
if(-not(Test-Path -LiteralPath $archive)){
 Invoke-WebRequest -Uri ('https://api.nuget.org/v3-flatcontainer/microsoft.web.webview2/'+$version+'/microsoft.web.webview2.'+$version+'.nupkg') -OutFile $archive
}
if((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $expectedHash){throw 'WebView2 SDK package hash mismatch; build refused.'}
if(-not(Test-Path -LiteralPath (Join-Path $cache 'lib\net462\Microsoft.Web.WebView2.Core.dll'))){Expand-Archive -LiteralPath $archive -DestinationPath $cache -Force}
$compiler=Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if(-not(Test-Path -LiteralPath $compiler)){throw 'The Windows .NET Framework C# compiler is required.'}
$source=Join-Path $workspace 'desktop\windows'
$core=Join-Path $cache 'lib\net462\Microsoft.Web.WebView2.Core.dll'
$forms=Join-Path $cache 'lib\net462\Microsoft.Web.WebView2.WinForms.dll'
$loader=Join-Path $cache 'runtimes\win-x64\native\WebView2Loader.dll'
$target=Join-Path $OutputDirectory ($(if($LocalStation){'AgentHomeStation.exe'}else{'WechatAgent.exe'}))
$arguments=@('/nologo','/target:winexe','/platform:x64','/optimize+','/utf8output',('/out:'+$target),('/win32manifest:'+(Join-Path $source 'app.manifest')),'/reference:System.Windows.Forms.dll','/reference:System.Drawing.dll','/reference:System.Net.Http.dll','/reference:System.Web.Extensions.dll',('/reference:'+$core),('/reference:'+$forms),('/resource:'+$core+',Microsoft.Web.WebView2.Core.dll'),('/resource:'+$forms+',Microsoft.Web.WebView2.WinForms.dll'),('/resource:'+$loader+',WebView2Loader.dll'),(Join-Path $source 'Deployment.cs'),(Join-Path $source 'WindowsRuntime.cs'),(Join-Path $source 'LocalStationRuntime.cs'),(Join-Path $source 'CloseSettings.cs'),(Join-Path $source 'CloseChoiceDialog.cs'),(Join-Path $source 'DesktopInstance.cs'),(Join-Path $source 'Program.cs'),(Join-Path $source 'MainForm.cs'))
if($LocalStation){$arguments+='/define:LOCAL_STATION'}
$icon=Join-Path $source 'assets\agent-chat-plugins.ico'
$arguments+=(Join-Path $source 'LifecycleSurface.cs')
if(-not(Test-Path -LiteralPath $icon)){throw 'The project desktop icon is missing.'}
$arguments+=('/win32icon:'+$icon)
& $compiler @arguments
if($LASTEXITCODE -ne 0){throw 'Desktop build failed.'}
if($LocalStation){
 $station=Join-Path $OutputDirectory 'agent-home-station-server.exe'
 Push-Location $workspace
 try { & go build -trimpath -o $station './cmd/agent-home-station' }
 finally { Pop-Location }
 if($LASTEXITCODE -ne 0){throw 'LocalStation server build failed.'}
}
Copy-Item -LiteralPath (Join-Path $cache 'LICENSE.txt') -Destination (Join-Path $OutputDirectory 'WebView2-LICENSE.txt')
if($Test){
 $tests=Join-Path $OutputDirectory 'DesktopTests.exe'
 & $compiler /nologo /target:exe /platform:x64 /utf8output ('/out:'+$tests) /reference:System.Net.Http.dll /reference:System.Web.Extensions.dll (Join-Path $source 'Deployment.cs') (Join-Path $source 'WindowsRuntime.cs') (Join-Path $source 'LocalStationRuntime.cs') (Join-Path $source 'CloseSettings.cs') (Join-Path $source 'DesktopInstance.cs') (Join-Path $source 'ControllerTests.cs')
 if($LASTEXITCODE -ne 0){throw 'Desktop tests did not compile.'}
 & $tests
 if($LASTEXITCODE -ne 0){throw 'Desktop controller tests failed.'}
 if($LocalStation){
  & $tests --local-cycle $OutputDirectory
  if($LASTEXITCODE -ne 0){throw 'LocalStation real process cycle failed.'}
 }
 $windowTests=Join-Path $OutputDirectory 'DesktopWindowTests.exe'
 $windowArguments=@('/target:exe','/main:WechatAgent.Desktop.WindowTests',('/out:'+$windowTests))
 $windowArguments+=@($arguments | Where-Object {$_ -notlike '/target:*' -and $_ -notlike '/out:*'})
 $windowArguments+=@((Join-Path $source 'ControllerTests.cs'),(Join-Path $source 'WindowTests.cs'))
 & $compiler @windowArguments
 if($LASTEXITCODE -ne 0){throw 'Desktop window tests did not compile.'}
 $windowRunner=Join-Path $OutputDirectory 'DesktopWindowTestRunner.exe'
 & $compiler /nologo /target:exe /platform:x64 /utf8output ('/out:'+$windowRunner) /reference:System.Net.Http.dll /reference:System.Web.Extensions.dll (Join-Path $source 'Deployment.cs') (Join-Path $source 'WindowsRuntime.cs') (Join-Path $source 'LocalStationRuntime.cs') (Join-Path $source 'WindowTestRunner.cs')
 if($LASTEXITCODE -ne 0){throw 'Isolated window test runner did not compile.'}
 & $windowRunner $windowTests
 if($LASTEXITCODE -ne 0){throw 'Desktop window lifecycle tests failed.'}
 if($LocalStation){
  & $windowRunner $windowTests --local-station $OutputDirectory
  if($LASTEXITCODE -ne 0){throw 'LocalStation desktop window test failed.'}
 }
}
# Incremental desktop updates need the matching plugin launch protocol too.
$broker=Join-Path $OutputDirectory 'runtimes\windows-agent-host'
New-Item -ItemType Directory -Force -Path $broker | Out-Null
foreach($name in @('windows_lpac.py','windows_plugin_host.pyw','windows_agent_host.pyw')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $broker $name)}
if($Portable){
 $resourceArgs=@{OutputDirectory=(Join-Path $OutputDirectory 'runtimes');MediaDirectory=$MediaDirectory;DocumentPythonPackagesDirectory=$DocumentPythonPackagesDirectory;DocumentTesseractDirectory=$DocumentTesseractDirectory;DocumentFontsDirectory=$DocumentFontsDirectory}
 & (Join-Path $PSScriptRoot 'Prepare-WindowsResources.ps1') @resourceArgs
 if($LASTEXITCODE -ne 0){throw 'Windows runtime resources build failed.'}
 if($LocalStation){
  Copy-Item -LiteralPath (Join-Path $workspace 'desktop\windows\PORTABLE-README.txt') -Destination (Join-Path $OutputDirectory '使用说明.txt')
  Get-Item -LiteralPath $target | Select-Object FullName,Length
  Get-FileHash -LiteralPath $target -Algorithm SHA256
  return
 }
 $nativeArgs=@{OutputDirectory=(Join-Path $OutputDirectory 'runtimes\windows-x64')}
 if($NativeSourceDirectory){$nativeArgs.SourceDirectory=$NativeSourceDirectory}
 if($Test){$nativeArgs.Test=$true}
 if($GoSourcePatch){$nativeArgs.GoSourcePatch=$GoSourcePatch}
 & (Join-Path $PSScriptRoot 'Build-WindowsNative.ps1') @nativeArgs
 if($LASTEXITCODE -ne 0){throw 'Windows native build failed.'}
 $music=Join-Path $OutputDirectory 'runtimes\windows-music'
 New-Item -ItemType Directory -Force -Path $music | Out-Null
 Get-ChildItem -LiteralPath $PSScriptRoot -File -Filter '*.py' | Where-Object {$_.Name -notlike 'test_*'} | ForEach-Object {Copy-Item -LiteralPath $_.FullName -Destination $music}
 foreach($name in @('gpt_sovits_windows_requirements.txt')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination $music}
 Copy-Item -LiteralPath (Join-Path $workspace 'components\yue2\app\workflows_api') -Destination (Join-Path $music 'workflows') -Recurse -Force
 $musicFiles=@{}
 Get-ChildItem -LiteralPath $music -Recurse -File | Where-Object Name -ne 'runtime-manifest.json' | ForEach-Object {$musicFiles[$_.FullName.Substring($music.Length+1)]=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLower()}
 [IO.File]::WriteAllText((Join-Path $music 'runtime-manifest.json'),(@{schema_version=1;platform='windows-x64';policy='registry-read-private-network-v1';files=$musicFiles}|ConvertTo-Json -Depth 6),[Text.UTF8Encoding]::new($false))
 Copy-Item -LiteralPath (Join-Path $workspace 'desktop\windows\PORTABLE-README.txt') -Destination (Join-Path $OutputDirectory '使用说明.txt')
}
Get-Item -LiteralPath $target | Select-Object FullName,Length
Get-FileHash -LiteralPath $target -Algorithm SHA256
