param(
    [int]$Port,
    [switch]$Cpu,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
$package = Split-Path -Parent $MyInvocation.MyCommand.Path
$release = Split-Path -Parent (Split-Path -Parent $package)
$id = (Get-Content -LiteralPath (Join-Path $package 'plugin.json') -Raw | ConvertFrom-Json).id
$registration = if ($id -eq 'yue') {
    Join-Path $release 'data\tools\yue\station-yue.json'
} else {
    Join-Path $release 'data\publictools\registrations\station-comfyui.json'
}
$runtime = Join-Path $package 'runtime\engine'
$python = Join-Path $runtime 'python\python.exe'
$entry = Join-Path $runtime 'ComfyUI\main.py'
$frontend = Join-Path $runtime 'python\Lib\site-packages\comfyui_frontend_package\static'
foreach ($file in @($python, $entry)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Package runtime is missing: $file" }
}
if ($Help) {
    & $python -s -B $entry --help
    exit $LASTEXITCODE
}
if (-not (Test-Path -LiteralPath $registration -PathType Leaf)) { throw "Tool registration is missing: $registration" }
$config = Get-Content -LiteralPath $registration -Raw | ConvertFrom-Json
if (-not $Port) { $Port = [int]$config.port }
if ($Port -lt 1024 -or $Port -gt 65535) { throw 'Port must be between 1024 and 65535' }
$private = Join-Path $release "data\publictools\standalone\$id"
foreach ($name in @('user', 'input', 'output', 'temp')) {
    New-Item -ItemType Directory -Force -Path (Join-Path $private $name) | Out-Null
}
$arguments = @('-s', '-B', $entry, '--listen', '127.0.0.1', '--port', $Port,
    '--disable-auto-launch', '--disable-all-custom-nodes', '--disable-api-nodes',
    '--base-directory', $private, '--user-directory', (Join-Path $private 'user'),
    '--models-directory', (Join-Path $package 'models'),
    '--input-directory', (Join-Path $private 'input'),
    '--output-directory', (Join-Path $private 'output'),
    '--temp-directory', (Join-Path $private 'temp'), '--front-end-root', $frontend)
if ($Cpu) { $arguments += '--cpu' }
elseif ($config.device -eq 'cuda') { $arguments += @('--cuda-device', [int]$config.cuda_device) }
& $python @arguments
if ($LASTEXITCODE -ne 0) { throw "$id runtime exited with code $LASTEXITCODE" }
