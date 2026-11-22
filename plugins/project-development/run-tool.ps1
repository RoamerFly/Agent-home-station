$ErrorActionPreference = 'Stop'
$package = Split-Path -Parent $MyInvocation.MyCommand.Path
$python = Join-Path $package 'runtime\python\python.exe'
$entry = Join-Path $package 'runtime\main.py'
if (-not (Test-Path -LiteralPath $python -PathType Leaf)) { throw "Tool package Python is missing: $python" }
if (-not (Test-Path -LiteralPath $entry -PathType Leaf)) { throw "Tool entry is missing: $entry" }
& $python -X utf8 $entry @args
if ($LASTEXITCODE -ne 0) { throw "Tool process failed with exit code $LASTEXITCODE" }
