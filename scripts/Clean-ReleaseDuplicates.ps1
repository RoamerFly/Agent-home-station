param([Parameter(Mandatory=$true)][string]$ReleaseRoot)
$ErrorActionPreference='Stop'
$PendingDirectory='E:\待删除'
$root=(Resolve-Path -LiteralPath $ReleaseRoot).ProviderPath.TrimEnd('\')
function Assert-Inside([string]$path){
 $resolved=(Resolve-Path -LiteralPath $path).ProviderPath
 if(-not $resolved.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase)){throw "Outside release: $path"}
 if((Get-Item -LiteralPath $resolved).Attributes -band [IO.FileAttributes]::ReparsePoint){throw "Linked path: $path"}
}
function Assert-Duplicate([string]$old,[string]$current,[switch]$OldRuntimeManifest){
 Assert-Inside $old; Assert-Inside $current
 foreach($entry in Get-ChildItem -LiteralPath $old -Recurse -Force){
  Assert-Inside $entry.FullName
  if($entry.PSIsContainer){continue}
  $relative=$entry.FullName.Substring($old.Length+1)
  $target=Join-Path $current $relative
  if(-not(Test-Path -LiteralPath $target -PathType Leaf)){throw "Unique file retained: $relative"}
  Assert-Inside $target
  if($OldRuntimeManifest -and $entry.Name -eq 'runtime-manifest.json'){continue}
  if((Get-FileHash -LiteralPath $entry.FullName).Hash -ne (Get-FileHash -LiteralPath $target).Hash){throw "Different file retained: $relative"}
 }
}
$toolRoot=Join-Path $root 'publictools'
$removals=@()
foreach($old in Get-ChildItem -LiteralPath $toolRoot -Directory){
 $matches=@(Get-ChildItem -LiteralPath $toolRoot -Directory | Where-Object {$_.Name.StartsWith($old.Name+'-')})
 if($matches.Count -ne 1){continue}
 Assert-Duplicate $old.FullName $matches[0].FullName
 $removals+=,$old.FullName
}
$nested=Join-Path $root 'env\env'
if(Test-Path -LiteralPath $nested){Assert-Duplicate $nested (Join-Path $root 'env') -OldRuntimeManifest; $removals+=,$nested}
# Refuse to mutate a live release. Paths are checked, never guessed from names.
$running=@(Get-CimInstance Win32_Process | Where-Object {$_.ExecutablePath -and $_.ExecutablePath.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase)})
if($running.Count){throw 'Stop the release through AgentHomeStation.exe --exit-services first.'}
New-Item -ItemType Directory -Path $PendingDirectory -Force | Out-Null
$pending=(Resolve-Path -LiteralPath $PendingDirectory).ProviderPath.TrimEnd('\')
if($pending -eq $root -or $pending.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'Pending directory must be outside release.'}
$batch=Join-Path $pending ('workstation-'+(Get-Date -Format 'yyyyMMdd-HHmmss')+'-'+[Guid]::NewGuid().ToString('N').Substring(0,8))
function Move-ToPending([string]$path){
 Assert-Inside $path
 $destination=Join-Path $batch $path.Substring($root.Length+1)
 if(-not [IO.Path]::GetFullPath($destination).StartsWith($pending+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'Outside pending directory.'}
 New-Item -ItemType Directory -Path (Split-Path $destination -Parent) -Force | Out-Null
 Move-Item -LiteralPath $path -Destination $destination
 Write-Output "Moved verified redundant item to: $destination"
}
foreach($path in $removals){Move-ToPending $path}
$obsolete=Join-Path $root 'agent-home-station-server.exe'
if(Test-Path -LiteralPath $obsolete){
 $current=Join-Path $root 'data\main_setting\runtime\agent-home-station-server.exe'
 if(-not(Test-Path -LiteralPath $current -PathType Leaf)){throw 'Canonical service executable missing.'}
 Assert-Inside $obsolete
 Move-ToPending $obsolete
 Write-Output 'Quarantined obsolete root service executable; canonical runtime retained.'
}
