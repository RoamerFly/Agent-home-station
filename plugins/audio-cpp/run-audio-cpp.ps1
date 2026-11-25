param(
    [ValidateSet('cli', 'server')][string]$Mode = 'cli',
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
)

$ErrorActionPreference = 'Stop'
$package = Split-Path -Parent $MyInvocation.MyCommand.Path
$engine = Join-Path $package 'runtime\engine'
$executable = Join-Path $engine "audiocpp_$Mode.exe"
if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) { throw "audio.cpp package executable is missing: $executable" }
if ($Mode -eq 'server' -and $Arguments.Count -eq 0) {
    $release = Split-Path -Parent (Split-Path -Parent $package)
    $config = Join-Path $release 'data\tools\audio-cpp\server.json'
    if (-not (Test-Path -LiteralPath $config -PathType Leaf)) { throw "audio.cpp server configuration is missing: $config" }
    $Arguments = @('--config', $config, '--ui', '--host', '127.0.0.1', '--port', '8192', '--backend', 'cuda')
}
$phonemizer = Join-Path $package 'runtime\phonemizer'
$oldPath = $env:PATH
$oldData = $env:ESPEAK_DATA_PATH
try {
    if (Test-Path -LiteralPath (Join-Path $phonemizer 'libespeak-ng.dll') -PathType Leaf) {
        $env:PATH = "$phonemizer;$oldPath"
        $env:ESPEAK_DATA_PATH = Join-Path $phonemizer 'espeak-ng-data'
    }
    & $executable @Arguments
    $result = $LASTEXITCODE
} finally {
    $env:PATH = $oldPath
    $env:ESPEAK_DATA_PATH = $oldData
}
if ($result -ne 0) { throw "audio.cpp exited with code $result" }
