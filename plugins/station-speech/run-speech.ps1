param(
    [Parameter(Mandatory = $true)][string]$VoiceId,
    [Parameter(Mandatory = $true)][string]$Text,
    [Parameter(Mandatory = $true)][string]$OutputFile,
    [ValidateSet('cpu', 'cuda')][string]$Device = 'cuda',
    [string]$Registration,
    [string]$ParametersFile,
    [switch]$Overwrite
)

$ErrorActionPreference = 'Stop'
$package = Split-Path -Parent $MyInvocation.MyCommand.Path
$release = Split-Path -Parent (Split-Path -Parent $package)
if (-not $Registration) { $Registration = Join-Path $release 'data\publictools\registrations\station-speech.json' }
$runtime = Join-Path $package 'runtime\engine'
$python = Join-Path $runtime 'python\python.exe'
$runner = Join-Path $package 'runtime\control\gpt_sovits.py'
$media = Join-Path $package 'runtime\media'
$project = Join-Path $package 'project'
$models = Join-Path $package 'models'
foreach ($path in @($Registration, $python, $runner)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Speech package resource is missing: $path" }
}
$registered = Get-Content -LiteralPath $Registration -Raw | ConvertFrom-Json
$voice = @($registered.voices | Where-Object { $_.id -eq $VoiceId })
if ($voice.Count -ne 1) { throw "Voice is not registered: $VoiceId" }
$voice = $voice[0]
$arguments = @('--project', $project, '--models', $models, '--packages', $runtime,
               '--gpt', $voice.gpt, '--sovits', $voice.sovits,
               '--reference', $voice.reference, '--prompt-text-file', $voice.prompt,
               '--text', $Text, '--output', $OutputFile, '--device', $Device,
               '--text-lang', $voice.text_language, '--prompt-lang', $voice.prompt_language)
if ($Device -eq 'cpu') { $arguments += '--no-half' }
if ($ParametersFile) { $arguments += @('--parameters', $ParametersFile) }
if ($Overwrite) { $arguments += '--overwrite' }
$oldPath = $env:PATH
$oldNLTK = $env:NLTK_DATA
try {
    $env:PATH = "$media;$oldPath"
    $env:NLTK_DATA = Join-Path $runtime 'nltk'
    & $python -X utf8 $runner @arguments
    $result = $LASTEXITCODE
} finally {
    $env:PATH = $oldPath
    $env:NLTK_DATA = $oldNLTK
}
if ($result -ne 0) { throw "Speech synthesis failed with exit code $result" }
