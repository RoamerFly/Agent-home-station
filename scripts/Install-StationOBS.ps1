[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string]$Archive,

    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string]$StationDataDirectory
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# This installer is deliberately offline.  It accepts only the pinned official
# portable archive and never touches a machine-wide OBS installation or an OBS
# user profile. The installed files are the complete immutable runtime
# inventory; the station creates its fresh runtime configuration elsewhere.
# OBS 33 core modules have runtime dependencies, so this intentionally retains
# the entire pinned core tree. The station disables the bundled WebSocket
# server in its fresh configuration and verifies listener state at runtime.
$expectedVersion = '33.0.1'
$expectedArchiveSha256 = '0d9436451c2d772b33428e3fdae9776f071895c335de17283176306fb72651d3'
$allowedRoots = @('bin', 'data', 'core')

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Test-Descendant([string]$Child, [string]$Parent) {
    $childFull = (Get-FullPath $Child).TrimEnd([char]92, [char]47)
    $parentFull = (Get-FullPath $Parent).TrimEnd([char]92, [char]47)
    if ($childFull.Equals($parentFull, [StringComparison]::OrdinalIgnoreCase)) { return $true }
    return $childFull.StartsWith($parentFull + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or
        $childFull.StartsWith($parentFull + [IO.Path]::AltDirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)
}

function Assert-NotReparsePoint([string]$Path, [string]$Label) {
    if (-not (Test-Path -LiteralPath $Path)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label must not be a symbolic link or reparse point: $Path"
    }
}

function Assert-PhysicalDirectoryChain([string]$Directory, [string]$Label) {
    $full = (Get-FullPath $Directory).TrimEnd([char]92, [char]47)
    $root = [IO.Path]::GetPathRoot($full).TrimEnd([char]92, [char]47)
    if ([string]::IsNullOrWhiteSpace($root)) { throw "$Label has no filesystem root: $Directory" }
    $cursor = $root + [IO.Path]::DirectorySeparatorChar
    $remainder = $full.Substring($cursor.Length)
    foreach ($part in $remainder -split '[\\/]') {
        if ([string]::IsNullOrWhiteSpace($part)) { continue }
        $cursor = Join-Path $cursor $part
        if (Test-Path -LiteralPath $cursor) { Assert-NotReparsePoint $cursor $Label }
    }
}

function New-PhysicalDirectory([string]$Directory, [string]$Label) {
    $full = Get-FullPath $Directory
    Assert-PhysicalDirectoryChain (Split-Path -Parent $full) $Label
    if (-not (Test-Path -LiteralPath $full)) {
        New-Item -ItemType Directory -Path $full -ErrorAction Stop | Out-Null
    }
    $item = Get-Item -LiteralPath $full -Force
    if (-not $item.PSIsContainer) { throw "$Label is not a directory: $full" }
    Assert-NotReparsePoint $full $Label
    return $full
}

function Assert-SafeStage([string]$StagePath, [string]$StationRoot) {
    $stageFull = Get-FullPath $StagePath
    if (-not (Test-Descendant $stageFull $StationRoot) -or (Split-Path -Parent $stageFull) -ne (Get-FullPath $StationRoot)) {
        throw "Refusing cleanup outside the station data directory: $stageFull"
    }
    Assert-NotReparsePoint $stageFull 'staging directory'
    if (Test-Path -LiteralPath $stageFull) {
        Get-ChildItem -LiteralPath $stageFull -Force -Recurse | ForEach-Object {
            if (($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing recursive cleanup through a staging reparse point: $($_.FullName)"
            }
        }
    }
}

function Get-ZipPathParts([string]$EntryName, [string]$ArchiveLabel) {
    $normalized = $EntryName.Replace([char]92, [char]47)
    if ([string]::IsNullOrWhiteSpace($normalized)) { return ,@() }
    if ($normalized.StartsWith('/') -or $normalized.StartsWith([string][char]92) -or $normalized -match '^[A-Za-z]:' -or $normalized -match '(^|/)\.\.(/|$)') {
        throw "Unsafe ZIP entry in ${ArchiveLabel}: $EntryName"
    }
    # Do not let PowerShell unwrap a one-component directory entry into a
    # scalar string: callers consistently need an indexable array.
    return ,@($normalized.Trim('/') -split '/')
}

function Test-ZipSymlink($Entry) {
    # ZIP external attributes use the upper 16 bits for Unix mode when present.
    # A symlink has file type 0120000.  OBS' official ZIP does not need links.
    $unixMode = (($Entry.ExternalAttributes -shr 16) -band 0xF000)
    return $unixMode -eq 0xA000
}

$archiveFull = Get-FullPath $Archive
if (-not (Test-Path -LiteralPath $archiveFull -PathType Leaf)) { throw "Offline OBS archive was not found: $archiveFull" }
Assert-NotReparsePoint $archiveFull 'archive'
$archiveStream = [IO.File]::Open($archiveFull, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
$stage = $null
try {
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        $archiveHash = ([BitConverter]::ToString($sha256.ComputeHash($archiveStream))).Replace('-', '').ToLowerInvariant()
    } finally { $sha256.Dispose() }
    if ($archiveHash -ne $expectedArchiveSha256) {
        throw "OBS archive SHA256 mismatch. Expected $expectedArchiveSha256, got $archiveHash."
    }
    $archiveStream.Position = 0

    $stationRoot = New-PhysicalDirectory $StationDataDirectory 'station data directory'
    $managedRoot = Join-Path $stationRoot 'managed-obs'
    $studioRoot = Join-Path $managedRoot 'studio'
    $installManifest = Join-Path $managedRoot 'install.json'
    Assert-PhysicalDirectoryChain $stationRoot 'station data directory'
    if (Test-Path -LiteralPath $managedRoot) {
        Assert-NotReparsePoint $managedRoot 'managed OBS directory'
        throw "Managed OBS is already installed. Refusing to overwrite: $managedRoot"
    }

    $stage = Join-Path $stationRoot ('.managed-obs-stage-' + [Guid]::NewGuid().ToString('N'))
    $stageManaged = Join-Path $stage 'managed-obs'
    $stageStudio = Join-Path $stageManaged 'studio'
    New-PhysicalDirectory $stageStudio 'private OBS staging directory' | Out-Null
    Assert-SafeStage $stage $stationRoot
    $skippedRoots = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    # Keep reading the exact file handle that was hashed above. FileShare.Read
    # prevents a concurrent writer from swapping or changing it mid-install.
    $zip = [IO.Compression.ZipArchive]::new($archiveStream, [IO.Compression.ZipArchiveMode]::Read, $true)
    try {
        $prefixes = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
        foreach ($entry in $zip.Entries) {
            $parts = Get-ZipPathParts $entry.FullName 'OBS archive'
            if ($parts.Count -eq 0) { continue }
            $first = $parts[0]
            if ($allowedRoots -contains $first) { [void]$prefixes.Add('') }
            elseif ($parts.Count -ge 2 -and $allowedRoots -contains $parts[1]) { [void]$prefixes.Add($first) }
        }
        if ($prefixes.Count -ne 1) { throw 'OBS archive must contain exactly one direct or one enclosing runtime root.' }
        $prefix = $null
        foreach ($candidatePrefix in $prefixes) { $prefix = $candidatePrefix; break }

        foreach ($entry in $zip.Entries) {
            $parts = Get-ZipPathParts $entry.FullName 'OBS archive'
            if ($parts.Count -eq 0) { continue }
            if ($prefix) {
                if ($parts[0] -ne $prefix) { [void]$skippedRoots.Add($parts[0]); continue }
                $parts = @($parts | Select-Object -Skip 1)
            }
            if ($parts.Count -eq 0) { continue }
            $root = $parts[0]
            if ($allowedRoots -notcontains $root) { [void]$skippedRoots.Add($root); continue }
            $relative = ($parts -join '/')
            if (Test-ZipSymlink $entry) { throw "OBS archive contains unsupported symbolic-link entry: $($entry.FullName)" }
            if ([string]::IsNullOrEmpty($entry.Name)) { continue }
            $destination = Join-Path $stageStudio ($relative.Replace('/', [IO.Path]::DirectorySeparatorChar))
            if (-not (Test-Descendant $destination $stageStudio)) { throw "ZIP entry escapes staging directory: $($entry.FullName)" }
            $parent = Split-Path -Parent $destination
            New-PhysicalDirectory $parent 'OBS extraction directory' | Out-Null
            if (Test-Path -LiteralPath $destination) { throw "Duplicate OBS archive entry: $($entry.FullName)" }
            $input = $entry.Open()
            try {
                $output = [IO.File]::Open($destination, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
                try { $input.CopyTo($output) } finally { $output.Dispose() }
            } finally { $input.Dispose() }
        }
    } finally { $zip.Dispose() }

    $requiredExe = Join-Path $stageStudio 'bin\64bit\obs64.exe'
    if (-not (Test-Path -LiteralPath $requiredExe -PathType Leaf)) { throw 'OBS portable archive did not provide bin/64bit/obs64.exe.' }
    foreach ($root in $allowedRoots) {
        $rootPath = Join-Path $stageStudio $root
        if (-not (Test-Path -LiteralPath $rootPath -PathType Container)) { throw "OBS portable archive did not provide required runtime root: $root" }
        Assert-PhysicalDirectoryChain $rootPath "extracted $root runtime root"
    }

    $files = [ordered]@{}
    foreach ($root in $allowedRoots) {
        $rootPath = Join-Path $stageStudio $root
        Get-ChildItem -LiteralPath $rootPath -Recurse -Force | ForEach-Object {
            if (($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Extracted OBS runtime contains a reparse point: $($_.FullName)" }
            if (-not $_.PSIsContainer) {
                # Keep the actual casing for fs.WalkDir verification. Hashes,
                # not paths, are normalized to lower case in the manifest.
                $relative = $_.FullName.Substring($stageStudio.Length).TrimStart([char]92, [char]47).Replace([char]92, [char]47)
                $files[$relative] = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            }
        }
    }
    if (-not $files.Contains('bin/64bit/obs64.exe')) { throw 'OBS runtime inventory is missing obs64.exe.' }
    $manifest = [ordered]@{ version = $expectedVersion; archive_sha256 = $archiveHash; files = $files }
    New-PhysicalDirectory $stageManaged 'private OBS staging directory' | Out-Null
    [IO.File]::WriteAllText((Join-Path $stageManaged 'install.json'), ($manifest | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))

    # The move is a same-volume directory rename because the stage is a direct
    # child of the requested station data directory.  Recheck immediately
    # before promotion to never overwrite an installation created concurrently.
    Assert-SafeStage $stage $stationRoot
    if (Test-Path -LiteralPath $managedRoot) { throw "Managed OBS appeared during installation; refusing overwrite: $managedRoot" }
    [IO.Directory]::Move($stageManaged, $managedRoot)
    if (-not (Test-Path -LiteralPath $installManifest -PathType Leaf) -or -not (Test-Path -LiteralPath $studioRoot -PathType Container)) {
        throw 'OBS promotion did not produce the required managed runtime layout.'
    }
    if (Test-Path -LiteralPath $stage) { Assert-SafeStage $stage $stationRoot; Remove-Item -LiteralPath $stage -Force }
    [pscustomobject]@{
        version = $expectedVersion
        archive_sha256 = $archiveHash
        install_directory = $studioRoot
        manifest = $installManifest
        skipped_archive_roots = @($skippedRoots | Sort-Object)
        obs_websocket_bundled = $true
    }
} catch {
    # Staging only contains files created by this invocation.  Do not remove a
    # path unless its resolved parent and attributes still prove that boundary.
    if ($null -ne $stage -and (Test-Path -LiteralPath $stage)) {
        Assert-SafeStage $stage $stationRoot
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
    throw
} finally {
    $archiveStream.Dispose()
}
