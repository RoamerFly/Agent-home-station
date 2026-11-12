param(
    [string]$OutputDirectory,
    [string]$DataDirectory,
    [string]$WeChatRuntimeDirectory,
    [switch]$Test
)

$ErrorActionPreference = 'Stop'
$workspace = Split-Path $PSScriptRoot -Parent
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $workspace 'dev\windows-station' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
if (-not $WeChatRuntimeDirectory) {
    $projectRoot = Split-Path (Split-Path $workspace -Parent) -Parent
    $defaultRuntime = Join-Path $projectRoot 'dist\agent-home-station\runtimes\windows-x64'
    if (Test-Path -LiteralPath (Join-Path $defaultRuntime 'wx.exe') -PathType Leaf) { $WeChatRuntimeDirectory = $defaultRuntime }
}
if (-not $WeChatRuntimeDirectory) { throw '请传入已核验的 -WeChatRuntimeDirectory。' }
$WeChatRuntimeDirectory = [IO.Path]::GetFullPath($WeChatRuntimeDirectory)

# The development package is an update target, not a disposable build
# directory. Build-Desktop.ps1 only replaces runtime/source files and never
# removes data or secrets, so an existing package keeps its owner identity,
# app accounts, chat records, models and browser profiles.
$dataBefore = $null
if ($DataDirectory) {
    $DataDirectory = [IO.Path]::GetFullPath($DataDirectory)
    if (-not (Test-Path -LiteralPath $DataDirectory -PathType Container)) {
        throw "复用资料目录不存在：$DataDirectory"
    }
    $dataBefore = (Get-Item -LiteralPath $DataDirectory).FullName
}

$build = Join-Path $PSScriptRoot 'Build-Desktop.ps1'
& $build -OutputDirectory $OutputDirectory -LocalStation -WithWeChatRuntime -WithWeChatApp -WithDouyinApp -WeChatRuntimeDirectory $WeChatRuntimeDirectory -Test:$Test
if ($LASTEXITCODE -ne 0) { throw '开发包构建失败。' }

$secrets = Join-Path $OutputDirectory 'secrets'
New-Item -ItemType Directory -Force -Path $secrets | Out-Null
$deploymentPath = Join-Path $secrets 'local-deployment.json'
$config = [ordered]@{
    backend = 'local-station'
    wechat_native_url = 'http://127.0.0.1:6174'
    wechat_native_enabled = $true
}
if ($DataDirectory) {
    $config.data_root = $DataDirectory
    $config.workspace_root = Join-Path $DataDirectory 'managed-workspaces'
}
if (Test-Path -LiteralPath $deploymentPath -PathType Leaf) {
    $existing = Get-Content -Raw -LiteralPath $deploymentPath | ConvertFrom-Json
    if ($existing.backend -and $existing.backend -ne 'local-station') { throw '开发包已有的部署后端不是 local-station。' }
    if ($existing.wechat_native_enabled -is [bool]) { $config.wechat_native_enabled = [bool]$existing.wechat_native_enabled }
    if ($existing.wechat_native_url) { $config.wechat_native_url = [string]$existing.wechat_native_url }
    if (-not $DataDirectory -and $existing.data_root) { $config.data_root = [string]$existing.data_root }
    if (-not $DataDirectory -and $existing.workspace_root) { $config.workspace_root = [string]$existing.workspace_root }
}
$config | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $deploymentPath -Encoding UTF8
Copy-Item -LiteralPath (Join-Path $workspace 'desktop\windows\STATION-DEVELOPMENT-README.txt') -Destination (Join-Path $OutputDirectory '开发包说明.txt') -Force

if ($dataBefore) {
    if (-not (Test-Path -LiteralPath $dataBefore -PathType Container)) { throw '开发包更新后资料目录消失，已停止交付。' }
    Write-Output "Development package updated; reused data directory: $dataBefore"
} else {
    Write-Output "Development package updated; persistent data directory: $(Join-Path $OutputDirectory 'data\station')"
}
Write-Output "Launcher: $(Join-Path $OutputDirectory 'AgentHomeStation.exe')"
