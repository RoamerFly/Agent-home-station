param(
 [Parameter(Mandatory=$true)][string]$FilePath,
 [string[]]$Arguments=@(),
 [string]$WorkingDirectory,
 [string]$StandardOutput,
 [string]$StandardError
)
$ErrorActionPreference='Stop'
$workspace=Split-Path $PSScriptRoot -Parent
if(-not $WorkingDirectory){$WorkingDirectory=$workspace}
$directory=Join-Path $workspace 'temp\launcher'
New-Item -ItemType Directory -Path $directory -Force | Out-Null
$request=Join-Path $directory ('hidden-'+[Guid]::NewGuid().ToString('N')+'.json')
if(-not $StandardOutput){$StandardOutput=Join-Path $directory 'background.stdout.log'}
if(-not $StandardError){$StandardError=Join-Path $directory 'background.stderr.log'}
$pythonw=(Get-Command pythonw.exe -ErrorAction Stop).Source
$data=@{arguments=@($FilePath)+$Arguments;cwd=$WorkingDirectory;stdout=$StandardOutput;stderr=$StandardError}
[IO.File]::WriteAllText($request,($data | ConvertTo-Json -Depth 4),[Text.UTF8Encoding]::new($false))
$info=[Diagnostics.ProcessStartInfo]::new()
$info.FileName=$pythonw
$info.Arguments='"'+(Join-Path $PSScriptRoot 'hidden_process.pyw')+'" --request "'+$request+'"'
$info.WorkingDirectory=$workspace
$info.UseShellExecute=$false
$info.CreateNoWindow=$true
$info.RedirectStandardInput=$true
$helper=[Diagnostics.Process]::Start($info)
$helper.StandardInput.Close()
$result=[IO.Path]::ChangeExtension($request,'result.json')
try {
 for($attempt=0;$attempt -lt 100;$attempt++){
  if(Test-Path -LiteralPath $result){
   $value=Get-Content -LiteralPath $result -Encoding UTF8 -Raw | ConvertFrom-Json
   if($value.error){throw $value.error}
   return $value
  }
  Start-Sleep -Milliseconds 100
 }
 throw 'Background process creation not confirmed; inspect the original request, do not replay.'
} finally {$helper.Dispose()}
