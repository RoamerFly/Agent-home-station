param([string]$OutputDirectory, [switch]$Test, [switch]$Portable, [string]$NativeSourceDirectory, [string]$MediaDirectory)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
if(-not $OutputDirectory){$OutputDirectory=Join-Path $workspace 'dist\windows-desktop'}
$OutputDirectory=[IO.Path]::GetFullPath($OutputDirectory)
$version='1.0.3537.50'
$expectedHash='5EA526BBD728ADDA0DA4D31219267E96460494A427E4894C4E09D9F320F4B9AA'
$cache=Join-Path $workspace ('temp\desktop-deps\webview2-'+$version)
$archive=$cache+'.zip'
New-Item -ItemType Directory -Force -Path (Split-Path $cache -Parent),$OutputDirectory | Out-Null
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
$target=Join-Path $OutputDirectory 'WechatAgent.exe'
$arguments=@('/nologo','/target:winexe','/platform:x64','/optimize+','/utf8output',('/out:'+$target),('/win32manifest:'+(Join-Path $source 'app.manifest')),'/reference:System.Windows.Forms.dll','/reference:System.Drawing.dll','/reference:System.Net.Http.dll','/reference:System.Web.Extensions.dll',('/reference:'+$core),('/reference:'+$forms),('/resource:'+$core+',Microsoft.Web.WebView2.Core.dll'),('/resource:'+$forms+',Microsoft.Web.WebView2.WinForms.dll'),('/resource:'+$loader+',WebView2Loader.dll'),(Join-Path $source 'Deployment.cs'),(Join-Path $source 'WindowsRuntime.cs'),(Join-Path $source 'CloseSettings.cs'),(Join-Path $source 'CloseChoiceDialog.cs'),(Join-Path $source 'Program.cs'),(Join-Path $source 'MainForm.cs'))
& $compiler @arguments
if($LASTEXITCODE -ne 0){throw 'Desktop build failed.'}
Copy-Item -LiteralPath (Join-Path $cache 'LICENSE.txt') -Destination (Join-Path $OutputDirectory 'WebView2-LICENSE.txt')
if($Test){
 $tests=Join-Path $OutputDirectory 'DesktopTests.exe'
 & $compiler /nologo /target:exe /platform:x64 /utf8output ('/out:'+$tests) /reference:System.Net.Http.dll /reference:System.Web.Extensions.dll (Join-Path $source 'Deployment.cs') (Join-Path $source 'WindowsRuntime.cs') (Join-Path $source 'CloseSettings.cs') (Join-Path $source 'ControllerTests.cs')
 if($LASTEXITCODE -ne 0){throw 'Desktop tests did not compile.'}
 & $tests
 if($LASTEXITCODE -ne 0){throw 'Desktop controller tests failed.'}
}
if($Portable){
 & (Join-Path $PSScriptRoot 'Prepare-WindowsResources.ps1') -OutputDirectory (Join-Path $OutputDirectory 'runtimes') -MediaDirectory $MediaDirectory
 $nativeArgs=@{OutputDirectory=(Join-Path $OutputDirectory 'runtimes\windows-x64')}
 if($NativeSourceDirectory){$nativeArgs.SourceDirectory=$NativeSourceDirectory}
 if($Test){$nativeArgs.Test=$true}
 & (Join-Path $PSScriptRoot 'Build-WindowsNative.ps1') @nativeArgs
 if($LASTEXITCODE -ne 0){throw 'Windows native build failed.'}
 $broker=Join-Path $OutputDirectory 'runtimes\windows-agent-host'
 New-Item -ItemType Directory -Force -Path $broker | Out-Null
 foreach($name in @('windows_agent_host.pyw','windows_lpac.py')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $broker $name)}
 Copy-Item -LiteralPath (Join-Path $workspace 'desktop\windows\PORTABLE-README.txt') -Destination (Join-Path $OutputDirectory '使用说明.txt')
}
Get-Item -LiteralPath $target | Select-Object FullName,Length
Get-FileHash -LiteralPath $target -Algorithm SHA256
