param(
    [Parameter(Mandatory = $true)][string]$InputFile,
    [Parameter(Mandatory = $true)][string]$OutputDirectory,
    [string]$Model = 'MGM_MAIN_v4.pth',
    [ValidateSet('auto', 'cpu', 'cuda')][string]$Device = 'auto',
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$AdditionalArguments
)

$ErrorActionPreference = 'Stop'
$package = Split-Path -Parent $MyInvocation.MyCommand.Path
$project = Join-Path $package 'project\rvc-upstream'
$python = Join-Path $package 'runtime\windows-music-python\python.exe'
$models = Join-Path $package 'models\separation'
if (-not (Test-Path -LiteralPath $python -PathType Leaf)) { throw "RVC package Python is missing: $python" }
if (-not (Test-Path -LiteralPath (Join-Path $project 'tools\pymss\cli.py') -PathType Leaf)) { throw "pymss is missing: $project" }
if (-not (Test-Path -LiteralPath $InputFile -PathType Leaf)) { throw "Input audio is missing: $InputFile" }

$oldPythonPath = $env:PYTHONPATH
$oldPath = $env:PATH
try {
    $env:PYTHONPATH = Join-Path $project 'tools'
    $env:PATH = "$project;$oldPath"
    Push-Location -LiteralPath $project
    try {
        & $python -m pymss.cli infer $Model --model-dir $models --input $InputFile --output $OutputDirectory --device $Device @AdditionalArguments
        $result = $LASTEXITCODE
    } finally {
        Pop-Location
    }
} finally {
    $env:PYTHONPATH = $oldPythonPath
    $env:PATH = $oldPath
}
if ($result -ne 0) { throw "pymss separation failed with exit code $result" }
