param(
 [Parameter(Mandatory=$true)][string]$FilePath,
 [string[]]$Arguments=@()
)
$ErrorActionPreference='Stop'
# Windows PowerShell 5 lacks ProcessStartInfo.ArgumentList. Quote argv for the
# Windows CRT, without cmd.exe, string evaluation or shell interpolation.
function Quote-NativeArgument([string]$Value){
 if($Value.Length -gt 0 -and $Value -notmatch '[\s"]'){return $Value}
 $value=[regex]::Replace($Value,'(\\*)"','$1$1\"')
 $value=[regex]::Replace($value,'(\\+)$','$1$1')
 return '"'+$value+'"'
}
$info=[Diagnostics.ProcessStartInfo]::new()
$info.FileName=$FilePath
$info.Arguments=($Arguments | ForEach-Object {Quote-NativeArgument $_}) -join ' '
$info.UseShellExecute=$false
$info.CreateNoWindow=$true
$info.RedirectStandardOutput=$true
$info.RedirectStandardError=$true
$info.RedirectStandardInput=$true
$info.StandardOutputEncoding=[Text.UTF8Encoding]::new($false)
$info.StandardErrorEncoding=[Text.UTF8Encoding]::new($false)
$process=[Diagnostics.Process]::new()
$process.StartInfo=$info
try {
 [void]$process.Start()
 $process.StandardInput.Close()
 $stdout=$process.StandardOutput.ReadToEndAsync()
 $stderr=$process.StandardError.ReadToEndAsync()
 $process.WaitForExit()
 Set-Variable -Name LASTEXITCODE -Scope 1 -Value $process.ExitCode
 $output=$stdout.GetAwaiter().GetResult()
 $errorText=$stderr.GetAwaiter().GetResult()
 if($errorText){Write-Verbose $errorText}
 if($output){Write-Output $output.TrimEnd()}
} finally {$process.Dispose()}
