$ErrorActionPreference = 'Stop'
$package = Split-Path -Parent $MyInvocation.MyCommand.Path
$python = Join-Path $package 'runtime\windows-music-python\python.exe'
$project = Join-Path $package 'project\rvc-upstream'

if (-not (Test-Path -LiteralPath $python -PathType Leaf)) {
    throw "RVC package Python is missing: $python"
}
if (-not (Test-Path -LiteralPath (Join-Path $project 'infer\cli.py') -PathType Leaf)) {
    throw "RVC project is missing: $project"
}

Push-Location -LiteralPath $project
try {
    & $python -m infer.cli @args
    $result = $LASTEXITCODE
} finally {
    Pop-Location
}
if ($result -ne 0) {
    throw "RVC command failed with exit code $result"
}
