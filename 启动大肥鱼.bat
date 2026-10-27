@echo off
setlocal
rem Double-click: start the existing services and open the management page.
rem Optional action: Start (default), Web, Music, Status.
if not exist "%~dp0scripts\Launch-Local.ps1" (
  echo Launcher script not found. Keep this BAT in the project root.
  pause
  exit /b 1
)
start "" "%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File "%~dp0scripts\Launch-Local.ps1" %*
exit /b
