param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)

$ErrorActionPreference = 'Stop'
$package = Split-Path -Parent $MyInvocation.MyCommand.Path
$executable = Join-Path $package 'runtime\engine\bin\64bit\obs64.exe'
if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) { throw "OBS executable is missing: $executable" }
$oldDirectory = Get-Location
try {
    Set-Location (Split-Path -Parent $executable)
    & $executable @Arguments
    $result = $LASTEXITCODE
} finally {
    Set-Location $oldDirectory
}
if ($result -ne 0) { throw "OBS exited with code $result" }
