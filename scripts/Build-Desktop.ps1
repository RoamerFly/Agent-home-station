param([string]$OutputDirectory, [switch]$Test, [switch]$Portable, [switch]$LocalStation, [switch]$WithWeChatApp, [switch]$WithWeChatRuntime, [switch]$WithShareReader, [switch]$WithMediaUnderstanding, [switch]$WithDocumentWorkshop, [switch]$WithAudioWorkshop, [switch]$WithRVCBackend, [switch]$WithComfyUI, [switch]$WithYuE, [switch]$WithStationSpeech, [switch]$WithAudioCpp, [switch]$WithOBSPreview, [string]$WeChatRuntimeDirectory, [string]$NativeSourceDirectory, [string]$MediaDirectory, [string]$GoSourcePatch, [string]$DocumentPythonPackagesDirectory, [string]$DocumentTesseractDirectory, [string]$DocumentFontsDirectory, [string]$DocumentPDFiumWheel, [string]$ToolPackagesDirectory, [string]$ApplicationPackagesDirectory, [string]$QuarantineDirectory, [switch]$WithBilibiliApp, [switch]$WithDouyinApp, [switch]$LegacyWechatDesktop, [switch]$WithExternalTools, [switch]$WithProjectDevelopment)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent

function Write-PortableReleaseManifest([string]$Root) {
 $checker=Join-Path $PSScriptRoot 'check_portable_release.py'
 if(-not(Test-Path -LiteralPath $checker -PathType Leaf)){throw 'Portable release contract checker is missing.'}
 & python -X utf8 $checker --release-root $Root --write-manifest
 if($LASTEXITCODE -ne 0){throw 'Portable release contract check failed.'}
 $migrationBuilder=Join-Path $PSScriptRoot 'build_migration_manifest.py'
 $migrationChecker=Join-Path $PSScriptRoot 'validate_migration_manifest.py'
 if(-not(Test-Path -LiteralPath $migrationBuilder -PathType Leaf) -or -not(Test-Path -LiteralPath $migrationChecker -PathType Leaf)){throw 'Migration manifest tools are missing.'}
 $registry=Join-Path $Root 'data\main_setting\registry'
 New-Item -ItemType Directory -Force -Path $registry | Out-Null
 $migrationManifest=Join-Path $registry 'migration-manifest.json'
 & python -X utf8 $migrationBuilder --release-root $Root --output $migrationManifest
 if($LASTEXITCODE -ne 0){throw 'Migration manifest generation failed.'}
 $migrationReport=Join-Path $registry 'migration-validation.json'
 & python -X utf8 $migrationChecker --release-root $Root --manifest $migrationManifest --output $migrationReport
 if($LASTEXITCODE -ne 0){throw 'Migration manifest validation failed.'}
}

function Get-ToolBusyEvidence([string]$ToolID,[string]$PackageDirectory,[string]$DataRoot) {
 $evidence=@()
 $package=[IO.Path]::GetFullPath($PackageDirectory)
 $processes=Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
  ($_.ExecutablePath -and $_.ExecutablePath.StartsWith($package,[StringComparison]::OrdinalIgnoreCase)) -or
  ($_.CommandLine -and $_.CommandLine.IndexOf($package,[StringComparison]::OrdinalIgnoreCase) -ge 0)
 }
 foreach($process in $processes){$evidence+='process '+$process.ProcessId}
 foreach($jobsRoot in @((Join-Path $DataRoot ('publictools\'+$ToolID+'\jobs')),(Join-Path $DataRoot ('tools\'+$ToolID+'\jobs')))){
  if(-not(Test-Path -LiteralPath $jobsRoot -PathType Container)){continue}
  foreach($file in Get-ChildItem -LiteralPath $jobsRoot -Recurse -File -Filter '*.json' -ErrorAction SilentlyContinue){
   try{$record=Get-Content -LiteralPath $file -Raw -Encoding UTF8 | ConvertFrom-Json}catch{continue}
   $status=[string]$record.status
   if($status -in @('queued','pending','running','retrying','waiting_tool','waiting_receipt')){$evidence+='job '+$file.Name}
  }
 }
 return $evidence
}

# Do not depend on the PowerShell Utility module being auto-loaded. The
# Windows PowerShell process started by the Python build/test harness can have
# a mixed PSModulePath, making Get-FileHash unavailable even though the
# interactive shell exposes it. Keep integrity checks deterministic by using
# the .NET hashing API directly.
function Get-Sha256Hex([string]$Path){
 if(-not(Test-Path -LiteralPath $Path -PathType Leaf)){throw ('Cannot hash missing file: '+$Path)}
 $sha=[System.Security.Cryptography.SHA256]::Create()
 try {
  $stream=[IO.File]::OpenRead($Path)
  try { return ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace('-','').ToLowerInvariant() }
  finally {$stream.Dispose()}
 } finally {$sha.Dispose()}
}

function Assert-CanonicalSource {
 $markerPath=Join-Path $workspace '.build-source.json'
 if(-not(Test-Path -LiteralPath $markerPath -PathType Leaf)){throw 'This checkout is not the registered canonical Windows build source.'}
 try {$marker=Get-Content -LiteralPath $markerPath -Raw | ConvertFrom-Json} catch {throw 'The canonical build source marker is invalid.'}
 if($marker.schema_version -ne 1 -or $marker.canonical_id -ne 'agent-home-station-windows' -or $marker.module -ne 'wechat-bot'){
  throw 'The selected checkout is not the registered canonical Windows build source.'
 }
 if(-not(Test-Path -LiteralPath (Join-Path $workspace 'go.mod') -PathType Leaf) -or -not(Test-Path -LiteralPath (Join-Path $workspace 'connected-apps\wechat\app.json') -PathType Leaf)){
  throw 'The canonical Windows build source is incomplete.'
 }
}

Assert-CanonicalSource

function Get-RelativeRuntimePath([string]$Root,[string]$Path){
 $rootPath=[IO.Path]::GetFullPath($Root).TrimEnd('\','/')
 $fullPath=[IO.Path]::GetFullPath($Path)
 if(-not $fullPath.StartsWith($rootPath+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'Runtime source file escaped the selected directory.'}
 return $fullPath.Substring($rootPath.Length+1).Replace('\','/')
}

function Assert-NoReparsePointAncestor([string]$Path){
 $candidate=[IO.Path]::GetFullPath($Path)
 while(-not(Test-Path -LiteralPath $candidate)){
  $parent=Split-Path -Path $candidate -Parent
  if(-not $parent -or $parent -eq $candidate){throw 'Cannot locate a physical parent directory for the runtime output.'}
  $candidate=$parent
 }
 while($candidate){
  $item=Get-Item -LiteralPath $candidate -Force
  if($item.Attributes -band [IO.FileAttributes]::ReparsePoint){throw 'The runtime output path contains a reparse-point ancestor and cannot be used.'}
  $parent=Split-Path -Path $candidate -Parent
  if(-not $parent -or $parent -eq $candidate){break}
  $candidate=$parent
 }
}

function Get-SourceProvenance {
 # Windows PowerShell can stop the native process early when Select-Object
 # takes its first output line. Read the complete single-value Git response.
 $commit=(& git -C $workspace rev-parse HEAD 2>$null)
 if($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($commit)){throw 'Unable to determine the canonical source commit.'}
 $remote=(& git -C $workspace config --get remote.origin.url 2>$null)
 $dirty=((& git -C $workspace status --porcelain 2>$null) | Where-Object {$_ -and $_.Trim()}).Count -gt 0
 return [ordered]@{
  source_root=(Split-Path $workspace -Leaf)
  source_commit=$commit.Trim()
  source_remote=([string]$remote).Trim()
  source_dirty=$dirty
  build_utc=(Get-Date).ToUniversalTime().ToString('o')
 }
}

function Test-LocalStationRuntimeIdentical([string]$Existing,[System.Collections.IDictionary]$ExpectedFiles,[string]$ExpectedNativeHash){
 if(-not(Test-Path -LiteralPath $Existing -PathType Container)){return $false}
 Assert-NoReparsePointAncestor $Existing
 $manifestPath=Join-Path $Existing 'windows-native-manifest.json'
 if(-not(Test-Path -LiteralPath $manifestPath -PathType Leaf)){return $false}
 Assert-NoReparsePointAncestor $manifestPath
 try {$manifest=Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json} catch {return $false}
 if($manifest.schema_version -ne 1 -or $manifest.platform -ne 'windows-x64' -or -not $manifest.local_station_runtime -or $manifest.native_sha256 -ne $ExpectedNativeHash){return $false}
 $actual=@{}
 foreach($file in @(Get-ChildItem -LiteralPath $Existing -Force)){
  if($file.PSIsContainer -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint)){return $false}
  $relative=Get-RelativeRuntimePath $Existing $file.FullName
  if($relative -eq 'windows-native-manifest.json'){continue}
  $actual[$relative]=Get-Sha256Hex $file.FullName
 }
 if($actual.Count -ne $ExpectedFiles.Count){return $false}
 foreach($entry in $ExpectedFiles.GetEnumerator()){
  if(-not $actual.ContainsKey($entry.Key) -or $actual[$entry.Key] -ne $entry.Value){return $false}
  $manifestFile=$manifest.files.PSObject.Properties[$entry.Key]
  if($null -eq $manifestFile -or $manifestFile.Value -ne $entry.Value){return $false}
 }
 return $true
}

function Stage-LocalStationWeChatRuntime([string]$SourceDirectory,[string]$PackageDirectory){
 if(-not $SourceDirectory){throw 'WithWeChatRuntime requires -WeChatRuntimeDirectory. Select only a previously verified windows-x64 runtime directory.'}
 $source=[IO.Path]::GetFullPath($SourceDirectory)
 if(-not(Test-Path -LiteralPath $source -PathType Container)){throw 'The selected WeChat runtime directory does not exist.'}
 Assert-NoReparsePointAncestor $source
 $entries=Get-ChildItem -LiteralPath $source -Force
 if($entries | Where-Object {$_.Attributes -band [IO.FileAttributes]::ReparsePoint}){throw 'The selected WeChat runtime contains a reparse point and cannot be staged.'}
 # A historical Windows release can contain the separate punctuation Python
 # environment.  That is an optional processing tool, not a wx.exe runtime;
 # copying it would silently pull an unrelated model and Python packages into
 # the lightweight station.  Stage only the native-client root files.
 if($entries | Where-Object {$_.PSIsContainer -and $_.Name -ne 'punctuation'}){throw 'The selected WeChat runtime contains an unexpected subdirectory.'}
 $files=@($entries | Where-Object {-not $_.PSIsContainer})
 $manifestPath=Join-Path $source 'windows-native-manifest.json'
 if(-not(Test-Path -LiteralPath $manifestPath -PathType Leaf)){throw 'The selected WeChat runtime has no windows-native-manifest.json.'}
 try {$sourceManifest=Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json} catch {throw 'The selected WeChat runtime manifest is not valid JSON.'}
 if($sourceManifest.schema_version -ne 1 -or $sourceManifest.platform -ne 'windows-x64'){throw 'The selected WeChat runtime manifest is not a Windows x64 native bundle.'}
 $wxSource=Join-Path $source 'wx.exe'
 if(-not(Test-Path -LiteralPath $wxSource -PathType Leaf)){throw 'The selected WeChat runtime has no wx.exe.'}
 $wxHash=Get-Sha256Hex $wxSource
 if([string]::IsNullOrWhiteSpace($sourceManifest.native_sha256) -or $sourceManifest.native_sha256.ToLower() -ne $wxHash){throw 'The selected WeChat runtime wx.exe does not match its manifest hash.'}
 $legacyAdminSource=Join-Path $source 'wechat-admin.exe'
 if(-not(Test-Path -LiteralPath $legacyAdminSource -PathType Leaf)){throw 'The selected WeChat runtime has no wechat-admin.exe for the legacy console.'}
 $legacyAdminHash=Get-Sha256Hex $legacyAdminSource
 if([string]::IsNullOrWhiteSpace($sourceManifest.admin_sha256) -or $sourceManifest.admin_sha256.ToLower() -ne $legacyAdminHash){throw 'The selected wechat-admin.exe does not match its manifest hash.'}

 $required=@('wx.exe','wechat-admin.exe','vcruntime140.dll','MSVC-REDISTRIBUTABLE-NOTICE.txt','agent-wechat-LICENSE.txt')
 $sourceNames=@($files | ForEach-Object {Get-RelativeRuntimePath $source $_.FullName})
 foreach($name in $required){if($sourceNames -notcontains $name){throw ('The selected WeChat runtime is incomplete: missing '+$name+'.')}}
 foreach($file in $files){
  $relative=Get-RelativeRuntimePath $source $file.FullName
   # The old management server is part of the LocalStation legacy-console
   # runtime; other native bundle files remain strictly allowlisted.
  if($relative -eq 'windows-native-manifest.json'){continue}
  $allowed=($relative -in @('wx.exe','wechat-admin.exe')) -or ($relative -match '(?i)\.dll$') -or ($relative -match '(?i)(license|notice).*\.txt$')
  if(-not $allowed){throw ('The selected WeChat runtime contains a file that is not a native dependency or notice: '+$relative)}
 }

 $runtimeRoot=Join-Path $PackageDirectory 'applications/wechat/runtime'
 # Existing external catalogs are read-only build inputs. Stage their native
 # overlay separately so the caller can apply it to the chosen catalog.
 if($ApplicationPackagesDirectory){$runtimeRoot=Join-Path $PackageDirectory 'env'}
 $destination=Join-Path $runtimeRoot 'windows-x64'
 Assert-NoReparsePointAncestor $PackageDirectory
 New-Item -ItemType Directory -Force -Path $runtimeRoot | Out-Null
 Assert-NoReparsePointAncestor $runtimeRoot
 $staging=$destination+'.staging-'+[Guid]::NewGuid().ToString('N')
 New-Item -ItemType Directory -Force -Path $staging | Out-Null
 $stagingCreated=$true
 try {
  $hashes=[ordered]@{}
  foreach($file in $files){
   $relative=Get-RelativeRuntimePath $source $file.FullName
   if($relative -eq 'windows-native-manifest.json'){continue}
   $target=Join-Path $staging $relative.Replace('/','\')
   New-Item -ItemType Directory -Force -Path (Split-Path $target -Parent) | Out-Null
   Copy-Item -LiteralPath $file.FullName -Destination $target
   $sourceHash=Get-Sha256Hex $file.FullName
   $targetHash=Get-Sha256Hex $target
   if($sourceHash -ne $targetHash){throw ('WeChat runtime copy hash mismatch: '+$relative)}
   $hashes[$relative]=$targetHash
  }
  # The console is our source-built component; native dependencies above
  # remain verified against their original bundle manifest.
  Push-Location $workspace
  try {
   & go build -buildvcs=false -trimpath -o (Join-Path $staging 'wechat-admin.exe') ./cmd/wechat-admin
   if($LASTEXITCODE -ne 0){throw 'Failed to build the current WeChat console.'}
  } finally { Pop-Location }
  $hashes['wechat-admin.exe']=Get-Sha256Hex (Join-Path $staging 'wechat-admin.exe')
  if($hashes['wx.exe'] -ne $wxHash){throw 'The staged wx.exe hash is not the validated native hash.'}
  $provenance=Get-SourceProvenance
  $record=[ordered]@{
   schema_version=1
   platform='windows-x64'
   local_station_runtime=$true
   native_source=$sourceManifest.native_source
   native_commit=$sourceManifest.native_commit
   native_patch_sha256=$sourceManifest.native_patch_sha256
   native_license_commit=$sourceManifest.native_license_commit
   native_sha256=$wxHash
   admin_sha256=$hashes['wechat-admin.exe']
   source_root=$provenance.source_root
   source_commit=$provenance.source_commit
   source_remote=$provenance.source_remote
   source_dirty=$provenance.source_dirty
   build_utc=$provenance.build_utc
   files=$hashes
   live_login_verified=$false
   live_delivery_verified=$false
  }
  [IO.File]::WriteAllText((Join-Path $staging 'windows-native-manifest.json'),($record | ConvertTo-Json -Depth 6),[Text.UTF8Encoding]::new($false))
  if((Get-Sha256Hex (Join-Path $staging 'wx.exe')) -ne $wxHash){throw 'The staged wx.exe failed its final hash validation.'}
  if(Test-Path -LiteralPath $destination){
   # A previous manual update could have left the temporary backup names that
   # Windows tools commonly create beside the native bundle. They are not part
   # of the verified runtime and would make the station's strict inventory
   # check reject an otherwise valid package. Remove only these two known
   # generated names; all other unexpected files remain a hard error.
   foreach($backupName in @('wechat-admin.exe~','wx.exe~')){
    $backupPath=Join-Path $destination $backupName
    if(Test-Path -LiteralPath $backupPath -PathType Leaf){
     Remove-Item -LiteralPath $backupPath -Force
     Write-Output ('Removed stale native runtime backup: '+$backupPath)
    }
   }
   if(Test-LocalStationRuntimeIdentical -Existing $destination -ExpectedFiles $hashes -ExpectedNativeHash $wxHash){
    Write-Output ('Verified existing WeChat native runtime: '+$destination+'; it was not replaced.')
    return
   }
   throw 'The package already contains a different WeChat runtime. Choose a fresh output directory; an existing runtime is never replaced.'
  }
  # Directory.Move refuses an already-created destination.  This prevents a
  # concurrent process from making Move-Item nest the staged directory into a
  # raced target after we performed the identical-bundle check above.
  [IO.Directory]::Move($staging,$destination)
  $stagingCreated=$false
 } finally {
  if($stagingCreated -and (Test-Path -LiteralPath $staging)){Remove-Item -LiteralPath $staging -Recurse -Force}
 }
 Write-Output ('Staged verified WeChat native runtime and legacy console: '+$destination+' ('+$files.Count+' files; no account data).')
}

function Stage-LocalStationSpeechRunner([string]$PackageDirectory){
 # Keep the trusted repository runner as the only source.  This package is a
 # preview pin for the registered backend, not a bundled Python environment,
 # model, voice profile or executable adapter.
 $source=Join-Path $workspace 'plugins\speech\runtime\gpt_sovits.py'
 if(-not(Test-Path -LiteralPath $source -PathType Leaf)){throw 'The trusted local speech runner is missing from plugins/speech/runtime.'}
 Assert-NoReparsePointAncestor $source
 $hash=Get-Sha256Hex $source
 $runtimeRoot=Join-Path $PackageDirectory 'publictools\station-speech-站内语音\runtime'
 $destination=Join-Path $runtimeRoot 'control'
 $manifestPath=Join-Path $destination 'runtime-manifest.json'
 Assert-NoReparsePointAncestor $PackageDirectory
 New-Item -ItemType Directory -Force -Path $runtimeRoot | Out-Null
 Assert-NoReparsePointAncestor $runtimeRoot
 $files=[ordered]@{'gpt_sovits.py'=$hash}
 $manifest=([ordered]@{schema_version=1;platform='windows-x64';policy='station-speech-runner-v1';source='plugins/speech/runtime/gpt_sovits.py';files=$files}|ConvertTo-Json -Depth 4)
 if(Test-Path -LiteralPath $destination){
  Assert-NoReparsePointAncestor $destination
  if(-not(Test-Path -LiteralPath $manifestPath -PathType Leaf) -or [IO.File]::ReadAllText($manifestPath) -cne $manifest){throw 'Existing station speech runner differs; use a fresh output directory.'}
  $entries=@(Get-ChildItem -LiteralPath $destination -Force)
  if($entries.Count -ne 2 -or $entries | Where-Object {$_.PSIsContainer -or ($_.Attributes -band [IO.FileAttributes]::ReparsePoint)}){throw 'Existing station speech runner has unexpected files.'}
  $staged=Join-Path $destination 'gpt_sovits.py'
  if(-not(Test-Path -LiteralPath $staged -PathType Leaf) -or ((Get-Sha256Hex $staged) -ne $hash)){throw 'Existing station speech runner integrity check failed.'}
  Write-Output ('Verified existing station speech runner: '+$staged+' (SHA-256 '+$hash+').')
  return
 }
 New-Item -ItemType Directory -Path $destination | Out-Null
 $staged=Join-Path $destination 'gpt_sovits.py'
 Copy-Item -LiteralPath $source -Destination $staged
 if((Get-Sha256Hex $staged) -ne $hash){throw 'Station speech runner source changed during staging.'}
 [IO.File]::WriteAllText($manifestPath,$manifest,[Text.UTF8Encoding]::new($false))
 Write-Output ('Staged trusted station speech runner: '+$staged+' (SHA-256 '+$hash+'). Backend registration must use this explicit absolute path and digest.')
}

# The standalone station is the product entry point. Keep the original
# -LocalStation switch for existing build commands.
if($LocalStation -and $LegacyWechatDesktop){throw 'LocalStation and LegacyWechatDesktop cannot be combined.'}
$LocalStation=-not $LegacyWechatDesktop
if($LocalStation -and ($NativeSourceDirectory -or $GoSourcePatch)){throw 'NativeSourceDirectory and GoSourcePatch require -LegacyWechatDesktop.'}
if($WithWeChatRuntime -and -not $LocalStation){throw 'WithWeChatRuntime is only valid with -LocalStation.'}
if($WithRVCBackend -and -not $LocalStation){throw 'WithRVCBackend is only valid with -LocalStation.'}
if($WithComfyUI -and -not $LocalStation){throw 'WithComfyUI is only valid with -LocalStation.'}
if($WithYuE -and -not $LocalStation){throw 'WithYuE is only valid with -LocalStation.'}
if($WithStationSpeech -and -not $LocalStation){throw 'WithStationSpeech is only valid with -LocalStation.'}
if($WithAudioCpp -and -not $LocalStation){throw 'WithAudioCpp is only valid with -LocalStation.'}
if($WithOBSPreview -and -not $LocalStation){throw 'WithOBSPreview is only valid with -LocalStation.'}
if($WithExternalTools -and -not $LocalStation){throw 'WithExternalTools is only valid with -LocalStation.'}
if($WithBilibiliApp -and -not $LocalStation){throw 'WithBilibiliApp is only valid with -LocalStation.'}
if($WithDouyinApp -and -not $LocalStation){throw 'WithDouyinApp is only valid with -LocalStation.'}
foreach($externalDirectory in @(@{Name='ToolPackagesDirectory';Value=$ToolPackagesDirectory},@{Name='ApplicationPackagesDirectory';Value=$ApplicationPackagesDirectory})){
 if(-not $externalDirectory.Value){continue}
 if(-not [IO.Path]::IsPathRooted($externalDirectory.Value)){throw ($externalDirectory.Name+' must be an absolute path.')}
 $externalPath=[IO.Path]::GetFullPath($externalDirectory.Value)
 if(-not (Test-Path -LiteralPath $externalPath -PathType Container)){throw ($externalDirectory.Name+' does not exist: '+$externalPath)}
 Assert-NoReparsePointAncestor $externalPath
}
if(-not $LocalStation -and ($ToolPackagesDirectory -or $ApplicationPackagesDirectory)){throw 'External tool/application package directories require a LocalStation build.'}
if($WithWeChatRuntime){$WithWeChatApp=$true}
# The standard station package exposes the installed Douyin application
# alongside WeChat. Its autonomous abilities remain separately authorized in
# the application settings; omitting the package here only makes the card
# disappear from the catalog.
if($LocalStation -and $WithWeChatApp){$WithDouyinApp=$true}
# A document converter is intentionally opt-in.  The three existing source
# directory arguments are the opt-in contract: when absent, a LocalStation
# document workshop remains able to create DOCX files but carries no PDF/OCR
# runtime.  Do not accept a partial set of sources.  The regular desktop
# catalog contains the supported tools, while a LocalStation catalog is opt-in;
# therefore only the latter must explicitly select document-workshop.
$hasDocumentRuntimeInput=[bool]($DocumentPythonPackagesDirectory -or $DocumentFontsDirectory -or $DocumentTesseractDirectory -or $DocumentPDFiumWheel)
if($LocalStation -and $hasDocumentRuntimeInput -and -not $WithDocumentWorkshop){throw 'Native document runtime sources require -WithDocumentWorkshop for a LocalStation package.'}
if($hasDocumentRuntimeInput -and (-not $DocumentPythonPackagesDirectory -or -not $DocumentFontsDirectory)){throw 'Native document runtime requires both DocumentPythonPackagesDirectory and DocumentFontsDirectory.'}
if(-not $OutputDirectory){$OutputDirectory=Join-Path $workspace ($(if($LocalStation){'dist\local-station-preview'}else{'dist\windows-desktop'}))}
$OutputDirectory=[IO.Path]::GetFullPath($OutputDirectory)
# Rebuilding is not a migration or a request to delete previous output.
foreach($relative in @('plugins\mobile-control','env\windows-music\mobile_bridge.py')){
 $obsolete=Join-Path $OutputDirectory $relative
 if(Get-Item -LiteralPath $obsolete -Force -ErrorAction SilentlyContinue){
  throw 'Output contains retired mobile components; build into a fresh directory.'
 }
}
if($LocalStation){
 $foreignOutput=@('WechatAgent.exe','runtimes\windows-x64','env\windows-music')
 # A Station build must never be layered over the retired mobile/phone
 # desktop output.  The old windows-x64 and windows-music trees contain a
 # second native host and can make the package appear to start while the
 # wrong product is actually serving the port.
 foreach($retired in @('runtimes\windows-music','env\windows-x64','env\windows-music','mobile_bridge.py')){
  $obsolete=Join-Path $OutputDirectory $retired
  if(Get-Item -LiteralPath $obsolete -Force -ErrorAction SilentlyContinue){
   throw 'Output contains retired mobile components; build into a fresh directory.'
  }
 }
}else{
 $foreignOutput=@('AgentHomeStation.exe','agent-home-station-server.exe')
}
foreach($relative in $foreignOutput){
 if(Get-Item -LiteralPath (Join-Path $OutputDirectory $relative) -Force -ErrorAction SilentlyContinue){
  throw 'Output belongs to a different desktop product; build into a fresh directory.'
 }
}
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
$version='1.0.3537.50'
$expectedHash='5EA526BBD728ADDA0DA4D31219267E96460494A427E4894C4E09D9F320F4B9AA'
$cache=Join-Path $workspace ('temp\desktop-deps\webview2-'+$version)
$archive=$cache+'.zip'
New-Item -ItemType Directory -Force -Path (Split-Path $cache -Parent),$OutputDirectory | Out-Null
# The zero-application preview deliberately has no optional tool catalog.  The
# regular desktop package retains the catalog validation/staging contract.
if(((-not $LocalStation) -or $WithWeChatApp -or $WithShareReader -or $WithMediaUnderstanding -or $WithDocumentWorkshop -or $WithAudioWorkshop -or $WithRVCBackend -or $WithComfyUI -or $WithYuE -or $WithStationSpeech -or $WithAudioCpp -or $WithOBSPreview -or $WithExternalTools -or $WithProjectDevelopment) -and -not ($LocalStation -and $ToolPackagesDirectory)){
 $pluginSource=Join-Path $workspace 'plugins'
 $catalogArgs=@('--source',$pluginSource,'--output',(Join-Path $OutputDirectory 'plugins'))
 if(-not $LocalStation){
  $supported=@(Get-ChildItem -LiteralPath $pluginSource -Directory | Where-Object Name -ne 'mobile-control')
  if(-not $supported.Count){throw 'No supported tools found for the Windows desktop catalog.'}
  foreach($package in $supported){$catalogArgs+=@('--package',$package.Name)}
 }
 # A WeChat application needs the read-only share and media capability
 # declarations in the package itself.  Previously LocalStation only staged
 # web-search, so the legacy console could list these packages from the
 # compiled catalog but could never install or invoke them.
 if($LocalStation -and $WithWeChatApp){$catalogArgs+=@('--package','share-reader','--package','media-understanding','--package','music','--package','speech')}
 if($LocalStation -and $WithShareReader -and -not $WithWeChatApp){$catalogArgs+=@('--package','share-reader')}
 if($LocalStation -and $WithMediaUnderstanding -and -not $WithWeChatApp){$catalogArgs+=@('--package','media-understanding')}
 if($LocalStation -and $WithDocumentWorkshop){$catalogArgs+=@('--package','document-workshop')}
 if($LocalStation -and $WithAudioWorkshop){$catalogArgs+=@('--package','audio-workshop')}
 if($LocalStation -and $WithRVCBackend){$catalogArgs+=@('--package','rvc')}
 if($LocalStation -and $WithComfyUI){$catalogArgs+=@('--package','comfyui')}
 if($LocalStation -and $WithYuE){$catalogArgs+=@('--package','yue')}
 if($LocalStation -and $WithStationSpeech){$catalogArgs+=@('--package','station-speech')}
 if($LocalStation -and $WithAudioCpp){$catalogArgs+=@('--package','audio-cpp')}
 if($LocalStation -and $WithOBSPreview){$catalogArgs+=@('--package','obs-preview')}
 if($LocalStation -and $WithExternalTools){$catalogArgs+=@('--package','external-tools')}
 if($LocalStation -and $WithProjectDevelopment){$catalogArgs+=@('--package','project-development')}
 if($LocalStation -and $WithWeChatApp){$catalogArgs+=@('--package','web-search')}
 & python -B (Join-Path $PSScriptRoot 'stage_plugin_catalog.py') @catalogArgs
 if($LASTEXITCODE -ne 0){throw 'Official plugin catalog staging failed; use a fresh output directory.'}
 if($LocalStation){
  # Package IDs stay stable; only the human-facing directory has a purpose suffix.
  $purposes=@{'audio-cpp'='语音识别与生成';'audio-workshop'='音频处理';'comfyui'='图像生成';'document-workshop'='文档处理';'external-tools'='外部工具';'media-understanding'='媒体理解';'music'='音乐生成与投递';'obs-preview'='直播预览';'project-development'='项目开发';'rvc'='换声';'share-reader'='分享内容读取';'speech'='语音处理';'station-speech'='站内语音';'visual-generation'='视觉生成';'web-search'='网络搜索';'yue'='作曲'}
  $publicRoot=Join-Path $OutputDirectory 'publictools'
  New-Item -ItemType Directory -Force -Path $publicRoot | Out-Null
  $stagedRoot=(Resolve-Path -LiteralPath (Join-Path $OutputDirectory 'plugins')).ProviderPath
  foreach($package in Get-ChildItem -LiteralPath $stagedRoot -Directory){
   $suffix=$purposes[$package.Name]
   if(-not $suffix){$suffix='公共工具'}
   $destination=Join-Path $publicRoot ($package.Name+'-'+$suffix)
   if($package.Parent.FullName -ne $stagedRoot){throw 'Unexpected tool source path.'}
   if(Test-Path -LiteralPath $destination){
    $dataRoot=Join-Path $OutputDirectory 'data'
    $busy=Get-ToolBusyEvidence $package.Name $destination $dataRoot
    if($busy.Count){throw ('工具包 '+$package.Name+' 当前不可替换，检测到 '+($busy -join ', ')+ '。请先等待任务完成或停止工具。')}
    if(-not $QuarantineDirectory){
     $QuarantineDirectory=Join-Path (Split-Path $OutputDirectory -Parent) ('.release-tool-replacements-'+[Guid]::NewGuid().ToString('N'))
    }
    New-Item -ItemType Directory -Force -Path $QuarantineDirectory | Out-Null
    $retired=Join-Path $QuarantineDirectory ($package.Name+'-'+(Get-Date -Format 'yyyyMMddHHmmssfff'))
    Move-Item -LiteralPath $destination -Destination $retired
   }
   Move-Item -LiteralPath $package.FullName -Destination $destination
  }
  Remove-Item -LiteralPath $stagedRoot # Empty staging container only.
 }

}
# Adding application source does not connect an account or enable its runtime.
$appIDs=@()
if((-not $LocalStation) -or $WithWeChatApp){$appIDs+='wechat'}
if($WithBilibiliApp){$appIDs+='bilibili'}
if($WithDouyinApp){$appIDs+='douyin'}
if($appIDs.Count -and -not ($LocalStation -and $ApplicationPackagesDirectory)){
 $appPackages=@(foreach($appID in $appIDs){'--package';$appID})
 $applicationCatalog = Join-Path $OutputDirectory $(if($LocalStation){'applications'}else{'connected-apps'})
 & python -B (Join-Path $PSScriptRoot 'stage_plugin_catalog.py') --source (Join-Path $workspace 'connected-apps') --output $applicationCatalog --descriptor app.json @appPackages
 if($LASTEXITCODE -ne 0){throw 'Application source staging failed; use a fresh output directory.'}
}
if(-not(Test-Path -LiteralPath $archive)){
 Invoke-WebRequest -Uri ('https://api.nuget.org/v3-flatcontainer/microsoft.web.webview2/'+$version+'/microsoft.web.webview2.'+$version+'.nupkg') -OutFile $archive
}
if((Get-Sha256Hex $archive).ToUpperInvariant() -ne $expectedHash){throw 'WebView2 SDK package hash mismatch; build refused.'}
if(-not(Test-Path -LiteralPath (Join-Path $cache 'lib\net462\Microsoft.Web.WebView2.Core.dll'))){Expand-Archive -LiteralPath $archive -DestinationPath $cache -Force}
$compiler=Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if(-not(Test-Path -LiteralPath $compiler)){throw 'The Windows .NET Framework C# compiler is required.'}
$source=Join-Path $workspace 'desktop\windows'
$wechatSupervisor=Join-Path $workspace 'connected-apps\wechat\adapter\windows\WechatStationRuntime.cs'
$core=Join-Path $cache 'lib\net462\Microsoft.Web.WebView2.Core.dll'
$forms=Join-Path $cache 'lib\net462\Microsoft.Web.WebView2.WinForms.dll'
$loader=Join-Path $cache 'runtimes\win-x64\native\WebView2Loader.dll'
$target=Join-Path $OutputDirectory ($(if($LocalStation){'AgentHomeStation.exe'}else{'WechatAgent.exe'}))
$arguments=@('/nologo','/target:winexe','/platform:x64','/optimize+','/utf8output',('/out:'+$target),('/win32manifest:'+(Join-Path $source 'app.manifest')),'/reference:System.Windows.Forms.dll','/reference:System.Drawing.dll','/reference:System.Net.Http.dll','/reference:System.Web.Extensions.dll',('/reference:'+$core),('/reference:'+$forms),('/resource:'+$core+',Microsoft.Web.WebView2.Core.dll'),('/resource:'+$forms+',Microsoft.Web.WebView2.WinForms.dll'),('/resource:'+$loader+',WebView2Loader.dll'),(Join-Path $source 'Deployment.cs'),(Join-Path $source 'WindowsRuntime.cs'),(Join-Path $source 'ProcessOwnership.cs'),(Join-Path $source 'LocalStationRuntime.cs'),$wechatSupervisor,(Join-Path $source 'CloseSettings.cs'),(Join-Path $source 'CloseChoiceDialog.cs'),(Join-Path $source 'DesktopInstance.cs'),(Join-Path $source 'DouyinAuthorizationDialog.cs'),(Join-Path $source 'ShareCredentialLoginDialog.cs'),(Join-Path $source 'LegacyWechatConsoleDialog.cs'),(Join-Path $source 'ApplicationWorkbenchDialog.cs'),(Join-Path $source 'Program.cs'),(Join-Path $source 'MainForm.cs'))
if($LocalStation){$arguments+='/define:LOCAL_STATION'}
$icon=Join-Path $source 'assets\agent-home.ico'
$arguments+=('/resource:'+(Join-Path $source 'assets\agent-home.png')+',AgentHomeStation.Icon.png')
$arguments+=(Join-Path $source 'LifecycleSurface.cs')
if(-not(Test-Path -LiteralPath $icon)){throw 'The project desktop icon is missing.'}
$arguments+=('/win32icon:'+$icon)
& $compiler @arguments
if($LASTEXITCODE -ne 0){throw 'Desktop build failed.'}
if($LocalStation){
 $stationRoot=Join-Path $OutputDirectory 'data\main_setting\runtime'
 New-Item -ItemType Directory -Force -Path $stationRoot | Out-Null
 $station=Join-Path $stationRoot 'agent-home-station-server.exe'
 $oldOS=$env:GOOS; $oldArch=$env:GOARCH
 $oldGitDir=$env:GIT_DIR; $oldGitWorkTree=$env:GIT_WORK_TREE
 Push-Location $workspace
 try {
  $env:GOOS='windows'; $env:GOARCH='amd64'
  & (Join-Path $workspace 'scripts\Test-WindowsDeviceRetirement.ps1') -SourceDirectory $workspace -Target Station -Test:$Test
  $gitStamp='-buildvcs=false'
  $revision=''
  if(Get-Command git -ErrorAction SilentlyContinue){
   $env:GIT_DIR=$null; $env:GIT_WORK_TREE=$null
   $gitRoot=& git rev-parse --show-toplevel 2>$null
   if($LASTEXITCODE -eq 0 -and [IO.Path]::GetFullPath($gitRoot) -eq [IO.Path]::GetFullPath($workspace)){
    # Go may otherwise use the enclosing repository to stamp a linked worktree.
    $env:GIT_DIR=& git rev-parse --absolute-git-dir
    if($LASTEXITCODE -ne 0){throw 'LocalStation worktree Git metadata is unavailable.'}
    $env:GIT_WORK_TREE=$workspace
    $revision=& git rev-parse HEAD
    if($LASTEXITCODE -ne 0){throw 'LocalStation worktree revision is unavailable.'}
    $gitStamp='-buildvcs=true'
   }
  }
  & go build $gitStamp -trimpath -o $station './cmd/agent-home-station'
  if($LASTEXITCODE -ne 0){throw 'LocalStation server build failed.'}
  if($revision){
   $metadata=& go version -m $station
   if($LASTEXITCODE -ne 0 -or -not ($metadata -match ('\bvcs\.revision='+[regex]::Escape($revision)+'$'))){throw 'LocalStation server build revision does not match the worktree.'}
  }
 } finally {
  $env:GOOS=$oldOS; $env:GOARCH=$oldArch
  $env:GIT_DIR=$oldGitDir; $env:GIT_WORK_TREE=$oldGitWorkTree
  Pop-Location
 }
 if($appIDs.Count -and -not $ApplicationPackagesDirectory){
  & $station verify-app-catalog --catalog $applicationCatalog @appIDs
  if($LASTEXITCODE -ne 0){throw 'Connected Apps do not match the built LocalStation server.'}
 }
 if($WithWeChatRuntime){Stage-LocalStationWeChatRuntime -SourceDirectory $WeChatRuntimeDirectory -PackageDirectory $OutputDirectory}
 if($WithStationSpeech){Stage-LocalStationSpeechRunner -PackageDirectory $OutputDirectory}
}
Copy-Item -LiteralPath (Join-Path $cache 'LICENSE.txt') -Destination (Join-Path $OutputDirectory 'WebView2-LICENSE.txt')
$gorillaLicense=Join-Path $workspace 'docs\licenses\gorilla-websocket.txt'
if(-not(Test-Path -LiteralPath $gorillaLicense -PathType Leaf)){throw 'The Gorilla WebSocket license notice is missing.'}
Copy-Item -LiteralPath $gorillaLicense -Destination (Join-Path $OutputDirectory 'Gorilla-WebSocket-LICENSE.txt')
Copy-Item -LiteralPath (Join-Path $workspace 'docs\licenses\rtmp-relay.txt') -Destination (Join-Path $OutputDirectory 'RTMP-Relay-LICENSE.txt') -ErrorAction Stop
$portableWeChatDefaults=$false
function Invoke-DesktopPackageTests {
 if(-not $Test){return}
 $tests=Join-Path $OutputDirectory 'DesktopTests.exe'
 & $compiler /nologo /target:exe /platform:x64 /utf8output ('/out:'+$tests) /reference:System.Net.Http.dll /reference:System.Web.Extensions.dll (Join-Path $source 'Deployment.cs') (Join-Path $source 'WindowsRuntime.cs') (Join-Path $source 'ProcessOwnership.cs') (Join-Path $source 'LocalStationRuntime.cs') $wechatSupervisor (Join-Path $source 'CloseSettings.cs') (Join-Path $source 'DesktopInstance.cs') (Join-Path $source 'ControllerTests.cs') (Join-Path $source 'CutoverRehearsal.cs')
 if($LASTEXITCODE -ne 0){throw 'Desktop tests did not compile.'}
 & $tests
 if($LASTEXITCODE -ne 0){throw 'Desktop controller tests failed.'}
 if($LocalStation){
  & $tests --local-cycle $OutputDirectory
  if($LASTEXITCODE -ne 0){throw 'LocalStation real process cycle failed.'}
  if($WithWeChatRuntime){
   $cycle=if($portableWeChatDefaults){'--portable-native-cycle'}else{'--local-native-cycle'}
   & $tests $cycle $OutputDirectory
   if($LASTEXITCODE -ne 0){throw 'LocalStation owned WeChat native cycle failed.'}
  }
 }
 $windowTests=Join-Path $OutputDirectory 'DesktopWindowTests.exe'
 $windowArguments=@('/target:exe','/main:WechatAgent.Desktop.WindowTests',('/out:'+$windowTests))
 $windowArguments+=@($arguments | Where-Object {$_ -notlike '/target:*' -and $_ -notlike '/out:*'})
 $windowArguments+=@((Join-Path $source 'ControllerTests.cs'),(Join-Path $source 'CutoverRehearsal.cs'),(Join-Path $source 'WindowTests.cs'))
 & $compiler @windowArguments
 if($LASTEXITCODE -ne 0){throw 'Desktop window tests did not compile.'}
 $windowRunner=Join-Path $OutputDirectory 'DesktopWindowTestRunner.exe'
 & $compiler /nologo /target:exe /platform:x64 /utf8output ('/out:'+$windowRunner) /reference:System.Net.Http.dll /reference:System.Web.Extensions.dll (Join-Path $source 'Deployment.cs') (Join-Path $source 'WindowsRuntime.cs') (Join-Path $source 'ProcessOwnership.cs') (Join-Path $source 'LocalStationRuntime.cs') $wechatSupervisor (Join-Path $source 'WindowTestRunner.cs')
 if($LASTEXITCODE -ne 0){throw 'Isolated window test runner did not compile.'}
 & $windowRunner $windowTests
 if($LASTEXITCODE -ne 0){throw 'Desktop window lifecycle tests failed.'}
 if($LocalStation){
  & $windowRunner $windowTests --local-station $OutputDirectory
  if($LASTEXITCODE -ne 0){throw 'LocalStation desktop window test failed.'}
 }
}
# Incremental desktop updates need the matching plugin launch protocol too.
 $broker=Join-Path $OutputDirectory 'env\windows-agent-host'
New-Item -ItemType Directory -Force -Path $broker | Out-Null
foreach($name in @('windows_lpac.py','windows_plugin_host.pyw','windows_agent_host.pyw','windows_speech_compat.py')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $broker $name)}
if($WithRVCBackend){
 $control=Join-Path $OutputDirectory 'publictools\rvc-换声\runtime\windows-station-rvc'
 $files=[ordered]@{}
 foreach($name in @('managed_music_runner.py','managed_music_service.py','managed_music_ipc.py','music_unix_client.py','music_account_scope.py','process_window.py','rvc_backend.py','rvc_cover.py',
                    'rvc_pitch.py','rvc_profiles.py','rvc_recovery.py','rvc_voice.py','rvc_windows.py','singing_config.py',
                    'singing_voice.py','station_rvc.py','windows_lpac.py','yue_bridge.py','yue_recovery.py','yue_sampling.py')){
  $files[$name]=Get-Sha256Hex (Join-Path $PSScriptRoot $name)
 }
 $sources=@{}
 foreach($name in $files.Keys){$sources[$name]=Join-Path $PSScriptRoot $name}
 foreach($workflow in Get-ChildItem -LiteralPath (Join-Path $workspace 'components\yue2\app\workflows_api') -File -Filter '*.json'){
  $name='workflows/'+$workflow.Name
  $sources[$name]=$workflow.FullName
  $files[$name]=Get-Sha256Hex $workflow.FullName
 }
 $manifest=([ordered]@{schema_version=1;platform='windows-x64';policy='station-rvc-v1';files=$files}|ConvertTo-Json -Depth 4)
 Assert-NoReparsePointAncestor $control
 $manifestPath=Join-Path $control 'runtime-manifest.json'
 if(Test-Path -LiteralPath $control){
  Assert-NoReparsePointAncestor $manifestPath
  if(-not(Test-Path -LiteralPath $manifestPath -PathType Leaf) -or [IO.File]::ReadAllText($manifestPath) -cne $manifest){
   throw 'Existing standalone RVC control package differs; use a fresh output directory.'
  }
  if(@(Get-ChildItem -LiteralPath $control -Recurse -File -Force).Count -ne ($files.Count+1)){throw 'Unexpected files in the standalone RVC control package.'}
  foreach($name in $files.Keys){
   $staged=Join-Path $control $name
   Assert-NoReparsePointAncestor $staged
   if((Get-Sha256Hex $staged) -ne $files[$name]){throw 'Standalone RVC control integrity check failed.'}
  }
 }else{
  New-Item -ItemType Directory -Path $control | Out-Null
  foreach($name in $files.Keys){
   $staged=Join-Path $control $name
   New-Item -ItemType Directory -Force -Path (Split-Path $staged -Parent) | Out-Null
   Copy-Item -LiteralPath $sources[$name] -Destination $staged
   if((Get-Sha256Hex $staged) -ne $files[$name]){throw 'Standalone RVC source changed during staging.'}
  }
  [IO.File]::WriteAllText($manifestPath,$manifest,[Text.UTF8Encoding]::new($false))
 }
}
# Document Workshop uses the same project-owned broker as every other local
# tool.  A normal LocalStation preview only needs the hash-pinned Python CLI;
# it deliberately carries no PDF/OCR dependencies.  Supplying the existing
# reviewed package and font source directories explicitly stages the bounded
# native PDF runtime beside that CLI.  Portable bundles use the shared resource
# preparer below, so avoid repeating either integrity-checked operation here.
if($LocalStation -and ($WithWeChatApp -or $WithDocumentWorkshop -or $WithAudioWorkshop -or $WithRVCBackend -or $WithStationSpeech -or $WithAudioCpp -or $WithMediaUnderstanding -or $WithExternalTools) -and -not $Portable){
 $runtimeRoot=Join-Path $OutputDirectory 'env'
 & python -X utf8 (Join-Path $PSScriptRoot 'prepare_windows_python.py') --target (Join-Path $runtimeRoot 'windows-python-cli') --cache (Join-Path $workspace 'temp\\windows-runtime-downloads')
 if($LASTEXITCODE -ne 0){throw 'Managed Windows Python preparation failed for the selected project tools.'}
 if($hasDocumentRuntimeInput){
  $documentArgs=@('--target',(Join-Path $runtimeRoot 'document-workshop'),'--python-packages-source',$DocumentPythonPackagesDirectory,'--fonts-source',$DocumentFontsDirectory)
  if($DocumentTesseractDirectory){$documentArgs+=@('--tesseract-source',$DocumentTesseractDirectory)}
  if($DocumentPDFiumWheel){$documentArgs+=@('--pdfium-wheel',$DocumentPDFiumWheel)}
  & python -X utf8 (Join-Path $PSScriptRoot 'prepare_document_runtime.py') @documentArgs
  if($LASTEXITCODE -ne 0){throw 'Project-local native document runtime preparation failed.'}
 }
}
if($LocalStation -and -not $Portable -and (Test-Path -LiteralPath (Join-Path $OutputDirectory 'publictools') -PathType Container)){
 & python -X utf8 (Join-Path $PSScriptRoot 'prepare_public_tool_runtimes.py') --release $OutputDirectory
 if($LASTEXITCODE -ne 0){throw 'Standalone public tool Python preparation failed.'}
}
if($LocalStation -and ($WithWeChatRuntime -or $ToolPackagesDirectory -or $ApplicationPackagesDirectory)){
 $dataRoot=[IO.Path]::GetFullPath((Join-Path $OutputDirectory 'data'))
 $settingsRoot=Join-Path $dataRoot 'main_setting'
 $deployment=Join-Path $settingsRoot 'local-deployment.json'
 Assert-NoReparsePointAncestor $deployment
 $existingDeployment=Get-Item -LiteralPath $deployment -Force -ErrorAction SilentlyContinue
 if($existingDeployment){
  try{$existingData=Get-Content -LiteralPath $deployment -Raw | ConvertFrom-Json}catch{throw 'Existing local-deployment.json is not valid JSON.'}
  foreach($entry in @(@{Name='tool_packages_directory';Value=$ToolPackagesDirectory},@{Name='application_packages_directory';Value=$ApplicationPackagesDirectory})){
   if($entry.Value -and [string]$existingData.($entry.Name) -ne [IO.Path]::GetFullPath($entry.Value)){throw ('Existing local-deployment.json has a different '+$entry.Name+'. Use a fresh output directory or edit the package configuration explicitly.')}
  }
 }else{
  New-Item -ItemType Directory -Force -Path $settingsRoot | Out-Null
  New-Item -ItemType Directory -Force -Path $dataRoot,(Join-Path $dataRoot 'applications'),(Join-Path $dataRoot 'publictools'),(Join-Path $dataRoot 'main_setting'),(Join-Path $dataRoot 'workspaces') | Out-Null
  $layoutPath=Join-Path $dataRoot 'data-layout.json'
  if(-not (Test-Path -LiteralPath $layoutPath -PathType Leaf)){
   [IO.File]::WriteAllText($layoutPath,'{"schema_version":1,"layout":"applications/publictools/tools/workspaces/main_setting"}',[Text.UTF8Encoding]::new($false))
  }
  $jsonData=[ordered]@{backend='local-station';data_root='data';workspace_root='data\workspaces'}
  if($WithWeChatRuntime -and $ApplicationPackagesDirectory){$jsonData.wechat_native_url='http://127.0.0.1:6174';$jsonData.wechat_native_enabled=$true}
  if($ToolPackagesDirectory){$jsonData.tool_packages_directory=[IO.Path]::GetFullPath($ToolPackagesDirectory)} elseif(Test-Path -LiteralPath (Join-Path $OutputDirectory 'publictools')){$jsonData.tool_packages_directory='publictools'}
  if($ApplicationPackagesDirectory){$jsonData.application_packages_directory=[IO.Path]::GetFullPath($ApplicationPackagesDirectory)} elseif(Test-Path -LiteralPath (Join-Path $OutputDirectory 'applications')){$jsonData.application_packages_directory='applications'}
  $json=($jsonData | ConvertTo-Json)
  $bytes=[Text.Encoding]::UTF8.GetBytes($json)
  $file=[IO.File]::Open($deployment,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
  try{$file.Write($bytes,0,$bytes.Length);$file.Flush($true)}finally{$file.Dispose()}
 }
 if($WithWeChatRuntime -and $Portable){$portableWeChatDefaults=$true}
}
if($Portable){
 $resourceArgs=@{OutputDirectory=(Join-Path $OutputDirectory 'env');MediaDirectory=$MediaDirectory;DocumentPythonPackagesDirectory=$DocumentPythonPackagesDirectory;DocumentTesseractDirectory=$DocumentTesseractDirectory;DocumentFontsDirectory=$DocumentFontsDirectory;DocumentPDFiumWheel=$DocumentPDFiumWheel}
 & (Join-Path $PSScriptRoot 'Prepare-WindowsResources.ps1') @resourceArgs
 if($LASTEXITCODE -ne 0){throw 'Windows runtime resources build failed.'}
 if($LocalStation -and (Test-Path -LiteralPath (Join-Path $OutputDirectory 'publictools') -PathType Container)){
  & python -X utf8 (Join-Path $PSScriptRoot 'prepare_public_tool_runtimes.py') --release $OutputDirectory
  if($LASTEXITCODE -ne 0){throw 'Standalone public tool Python preparation failed.'}
 }
  if($LocalStation){
  Copy-Item -LiteralPath (Join-Path $workspace 'desktop\windows\STATION-PORTABLE-README.txt') -Destination (Join-Path $OutputDirectory 'README.txt')
  Write-PortableReleaseManifest $OutputDirectory
  Invoke-DesktopPackageTests
  Get-Item -LiteralPath $target | Select-Object FullName,Length
  [ordered]@{Algorithm='SHA256';Hash=(Get-Sha256Hex $target);Path=$target}
  return
 }
 $nativeArgs=@{OutputDirectory=(Join-Path $OutputDirectory 'env\windows-x64')}
 if($NativeSourceDirectory){$nativeArgs.SourceDirectory=$NativeSourceDirectory}
 if($Test){$nativeArgs.Test=$true}
 if($GoSourcePatch){$nativeArgs.GoSourcePatch=$GoSourcePatch}
 & (Join-Path $PSScriptRoot 'Build-WindowsNative.ps1') @nativeArgs
 if($LASTEXITCODE -ne 0){throw 'Windows native build failed.'}
 $music=Join-Path $OutputDirectory 'env\windows-music'
 New-Item -ItemType Directory -Force -Path $music | Out-Null
 Get-ChildItem -LiteralPath $PSScriptRoot -File -Filter '*.py' | Where-Object {$_.Name -notlike 'test_*' -and $_.Name -ne 'mobile_bridge.py'} | ForEach-Object {Copy-Item -LiteralPath $_.FullName -Destination $music}
 foreach($name in @('gpt_sovits_windows_requirements.txt')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination $music}
 Copy-Item -LiteralPath (Join-Path $workspace 'components\yue2\app\workflows_api') -Destination (Join-Path $music 'workflows') -Recurse -Force
 $musicFiles=@{}
 Get-ChildItem -LiteralPath $music -Recurse -File | Where-Object Name -ne 'runtime-manifest.json' | ForEach-Object {$musicFiles[$_.FullName.Substring($music.Length+1)]=Get-Sha256Hex $_.FullName}
 [IO.File]::WriteAllText((Join-Path $music 'runtime-manifest.json'),(@{schema_version=1;platform='windows-x64';policy='registry-read-private-network-v1';files=$musicFiles}|ConvertTo-Json -Depth 6),[Text.UTF8Encoding]::new($false))
 Copy-Item -LiteralPath (Join-Path $workspace 'desktop\windows\PORTABLE-README.txt') -Destination (Join-Path $OutputDirectory '使用说明.txt')
}
if($LocalStation){Write-PortableReleaseManifest $OutputDirectory}
Invoke-DesktopPackageTests
Get-Item -LiteralPath $target | Select-Object FullName,Length
[ordered]@{Algorithm='SHA256';Hash=(Get-Sha256Hex $target);Path=$target}
