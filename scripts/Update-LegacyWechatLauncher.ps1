[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Workspace,
    [Parameter(Mandatory)][string]$Launcher,
    [Parameter(Mandatory)][string]$Replacement,
    [Parameter(Mandatory)][ValidatePattern('\A[0-9a-fA-F]{64}\z')][string]$ReplacementSha256,
    [switch]$Restore,
    [ValidatePattern('\A[0-9a-fA-F]{64}\z')][string]$ApplyDigest
)

$ErrorActionPreference = 'Stop'

function LocalPath([string]$Path) {
    if ($Path -notmatch '\A[A-Z]:[\\/]' -or $Path.Substring(2).Contains(':')) {
        throw '路径必须是本机盘符下的绝对路径。'
    }
    $full = [IO.Path]::GetFullPath($Path)
    $drive = [IO.DriveInfo]::new([IO.Path]::GetPathRoot($full))
    if ($drive.DriveType -ne [IO.DriveType]::Fixed) { throw '仅支持本机固定卷。' }
    return $full
}

function CheckedItem([string]$Path, [switch]$Directory, [switch]$Optional) {
    try { $item = Get-Item -LiteralPath $Path -Force }
    catch [System.Management.Automation.ItemNotFoundException] {
        if ($Optional) { return $null }
        throw
    }
    if ($item.PSIsContainer -ne $Directory.IsPresent) { throw "文件类型不符：$Path" }
    for ($parent = $item; $null -ne $parent;) {
        if ($parent.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "拒绝重解析点：$($parent.FullName)" }
        if ($parent -is [IO.FileInfo]) { $parent = $parent.Directory } else { $parent = $parent.Parent }
    }
    return $item
}

function FileHash([string]$Path, [switch]$Optional) {
    $item = CheckedItem $Path -Optional:$Optional
    if ($null -eq $item) { return $null }
    return (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
}

function ContentHash([byte[]]$Bytes) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return [BitConverter]::ToString($sha.ComputeHash($Bytes)).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function ReadJson([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    $json = [Text.Encoding]::UTF8.GetString($bytes).TrimStart([char]0xfeff) | ConvertFrom-Json
    return @{ value = $json; hash = (ContentHash $bytes) }
}

function ServicePort([string]$Value, [int]$DefaultPort) {
    if (-not $Value) { return $DefaultPort }
    $url = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$url) -or $url.Scheme -ne 'http' -or
        $url.Host -ne '127.0.0.1' -or $url.AbsolutePath -ne '/' -or $url.UserInfo -or $url.Query -or $url.Fragment -or $url.Port -lt 1) {
        throw '旧端必须使用有效的本机回环服务地址。'
    }
    return $url.Port
}

function AssertStopped([int[]]$Ports) {
    if (Get-Process -Name WechatAgent, wx, wechat-admin, wechat-bot, wechat-sendd -ErrorAction SilentlyContinue) {
        throw '仍有旧微信进程运行，请先正常停止。'
    }
    $listeners = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners()
    foreach ($listener in $listeners) {
        if ($listener.Port -in $Ports) { throw '旧端服务端口仍被占用，请先正常停止。' }
    }
}

function ReadDeployment {
    $path = Join-Path $Workspace 'secrets/local-deployment.json'
    $configuration = $null
    if (CheckedItem (Join-Path $Workspace 'secrets') -Directory -Optional) {
        if (CheckedItem $path -Optional) { $configuration = ReadJson $path }
    }
    $config = $configuration.value
    if (($configuration -and ($config.backend -cne 'windows-native' -or $config.music_service -or
        ($config.account -and $config.account -notmatch '\A[A-Za-z0-9_-]+\z'))) -or
        (-not $configuration -and (CheckedItem (Join-Path $Workspace 'agent-home-station-server.exe') -Optional))) {
        throw '仅支持旧版 Windows 原生微信部署。'
    }
    $data = $config.data_root
    if (-not $data) { $data = 'data' }
    $runtime = $config.runtime_directory
    if (-not $runtime) { $runtime = 'runtimes/windows-x64' }
    $data = LocalPath ([IO.Path]::Combine($Workspace, $data))
    $runtime = LocalPath ([IO.Path]::Combine($Workspace, $runtime))
    $null = CheckedItem $data -Directory
    $null = CheckedItem $runtime -Directory
    $ports = @((ServicePort $config.admin_url 8081), (ServicePort $config.native_url 6174))
    if ($ports[0] -eq $ports[1]) { throw '旧端的管理与原生服务必须使用不同端口。' }
    return @{ data = $data; runtime = $runtime; ports = $ports; config_hash = $configuration.hash }
}

function HasCutoverMarkers([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    $aligned = [Text.Encoding]::Unicode.GetString($bytes)
    $unaligned = [Text.Encoding]::Unicode.GetString($bytes, 1, $bytes.Length - 1)
    foreach ($marker in @('legacy-cutover-state.json', 'legacy-cutover.lock', 'legacy-cutover-claim.json')) {
        if (-not $aligned.Contains($marker) -and -not $unaligned.Contains($marker)) { return $false }
    }
    return $true
}

function ReadPlan {
    $deployment = ReadDeployment
    if (CheckedItem (Join-Path $deployment.data 'legacy-cutover-claim.json') -Optional) { throw '微信交接声明仍有效，不能更换旧端启动器。' }
    $manifestPath = Join-Path $deployment.runtime 'windows-native-manifest.json'
    $null = CheckedItem $manifestPath
    $manifest = ReadJson $manifestPath
    if ($manifest.value.schema_version -ne 1 -or $manifest.value.platform -cne 'windows-x64' -or $manifest.value.local_station_runtime) {
        throw 'Windows 旧端运行清单不兼容。'
    }
    $bundleHashes = [ordered]@{ 'wx.exe' = $manifest.value.native_sha256; 'wechat-admin.exe' = $manifest.value.admin_sha256 }
    if ($null -ne $manifest.value.files) {
        foreach ($entry in $manifest.value.files.PSObject.Properties) {
            $target = LocalPath ([IO.Path]::Combine($deployment.runtime, $entry.Name))
            if (-not $target.StartsWith($deployment.runtime.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase) -or
                $entry.Value -ne (FileHash $target)) { throw '旧端运行清单中的文件校验失败。' }
        }
    }
    foreach ($name in $bundleHashes.Keys) {
        if ($bundleHashes[$name] -ne (FileHash (Join-Path $deployment.runtime $name))) { throw "旧端运行文件校验失败：$name" }
    }
    AssertStopped $deployment.ports
    $previousHash = FileHash $Launcher
    $replacementHash = FileHash $Replacement
    if ($previousHash -eq $replacementHash -or $replacementHash -ne $ReplacementSha256) { throw '替换文件相同，或 SHA-256 与确认值不符。' }
    foreach ($executable in @($Launcher, $Replacement)) {
        if ([Reflection.AssemblyName]::GetAssemblyName($executable).Name -cne 'WechatAgent') { throw '启动器程序集标识不符。' }
    }
    if ($Restore) {
        if ([IO.Path]::GetDirectoryName($Replacement) -ne [IO.Path]::GetDirectoryName($Launcher) -or
            [IO.Path]::GetFileName($Replacement) -notmatch "\AWechatAgent\.previous-$replacementHash-[a-f0-9]{32}\.exe\z") {
            throw '恢复必须使用本工具在同一目录留下且哈希匹配的原启动器。'
        }
    } elseif (-not (HasCutoverMarkers $Replacement)) {
        throw '候选启动器缺少停机交接能力标记。'
    }
    $proof = Join-Path $deployment.data 'legacy-cutover-state.json'
    $proofHash = FileHash $proof -Optional
    $invalidateProof = $proofHash -and ($Restore -or -not (HasCutoverMarkers $Launcher))
    $binding = [ordered]@{
        restore = $Restore.IsPresent; workspace = $Workspace; launcher = $Launcher; replacement = $Replacement
        previous_sha256 = $previousHash; replacement_sha256 = $replacementHash; config = $deployment.config_hash
        data = $deployment.data; runtime = $deployment.runtime; ports = $deployment.ports; manifest = $manifest.hash
        stop_proof = $proofHash; invalidate_proof = $invalidateProof
    }
    $digest = ContentHash ([Text.Encoding]::UTF8.GetBytes(($binding | ConvertTo-Json -Compress)))
    return @{ digest = $digest; binding = $binding; proof = $proof }
}

$Workspace = LocalPath $Workspace
$Launcher = LocalPath $Launcher
$Replacement = LocalPath $Replacement
$null = CheckedItem $Workspace -Directory
if ([IO.Path]::GetFileName($Launcher) -ine 'WechatAgent.exe') { throw '目标必须是旧部署的 WechatAgent.exe。' }

$mutex = [Threading.Mutex]::new($false, 'Local\RoamerFly.WechatAgent.Desktop.v2')
$ownsMutex = $false
$dataLock = $null
$ownsDataLock = $false
$stage = $null
try {
    try { $ownsMutex = $mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $ownsMutex = $true }
    if (-not $ownsMutex) { throw '旧微信桌面仍占用启动互斥锁。' }
    $deployment = ReadDeployment
    $lockPath = Join-Path $deployment.data 'legacy-cutover.lock'
    if (CheckedItem $lockPath -Optional) {
        $dataLock = [IO.File]::Open($lockPath, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::ReadWrite)
        try { $dataLock.Lock(0, 1) }
        catch [IO.IOException] { throw '旧端运行锁仍被占用，请先正常停止。' }
        $ownsDataLock = $true
    }
    $plan = ReadPlan
    if ($deployment.data -ne $plan.binding.data -or (-not $dataLock -and (CheckedItem $lockPath -Optional))) {
        throw '旧端数据根或运行锁发生变化，请重新预演。'
    }
    $result = [ordered]@{
        state = 'ready'; apply_digest = $plan.digest; rollback_file = $null; invalidated_stop_proof = $null
        previous_sha256 = $plan.binding.previous_sha256; replacement_sha256 = $plan.binding.replacement_sha256
    }
    if ($ApplyDigest) {
        if ($ApplyDigest -ne $plan.digest) { throw '预演摘要已经变化，请重新预演。' }
        $folder = [IO.Path]::GetDirectoryName($Launcher)
        $nonce = [Guid]::NewGuid().ToString('N')
        $stagePath = Join-Path $folder "WechatAgent.staged-$nonce.exe"
        $stagedFile = [IO.File]::Open($stagePath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        $stage = $stagePath
        try {
            $source = [IO.File]::OpenRead($Replacement)
            try { $source.CopyTo($stagedFile); $stagedFile.Flush($true) } finally { $source.Dispose() }
        } finally { $stagedFile.Dispose() }
        if ((FileHash $stage) -ne $result.replacement_sha256 -or (ReadPlan).digest -ne $plan.digest -or
            (-not $dataLock -and (CheckedItem $lockPath -Optional))) { throw '暂存后发现文件或运行状态变化，请重新预演。' }
        if ($plan.binding.invalidate_proof) {
            $mode = if ($Restore) { 'restore' } else { 'update' }
            $archived = Join-Path $plan.binding.data "legacy-cutover-state.launcher-$mode-$nonce.json"
            [IO.File]::Move($plan.proof, $archived)
            $result.invalidated_stop_proof = $archived
        }
        $rollback = Join-Path $folder "WechatAgent.previous-$($result.previous_sha256)-$nonce.exe"
        # Keep the staged file for inspection if Windows reports a partial exchange.
        $stage = $null
        try { [IO.File]::Replace($stagePath, $Launcher, $rollback) }
        catch { throw "Windows 未确认替换结果。启动器 $Launcher，原文件副本 $rollback，暂存 $stagePath。请核对文件后再启动。$($_.Exception.Message)" }
        if ((FileHash $Launcher -Optional) -ne $result.replacement_sha256 -or (FileHash $rollback -Optional) -ne $result.previous_sha256) {
            throw "启动器或原文件副本的替换后校验失败。启动器 $Launcher，原文件副本 $rollback。请核对后再启动。"
        }
        $result.rollback_file = $rollback
        $result.state = if ($Restore) { 'restored' } else { 'updated' }
    }
    $result | ConvertTo-Json -Compress
} finally {
    try {
        if ($stage) { [IO.File]::Delete($stage) }
    } finally {
        try {
            if ($dataLock) {
                try { if ($ownsDataLock) { $dataLock.Unlock(0, 1) } } finally { $dataLock.Dispose() }
            }
        } finally {
            try { if ($ownsMutex) { $mutex.ReleaseMutex() } } finally { $mutex.Dispose() }
        }
    }
}
