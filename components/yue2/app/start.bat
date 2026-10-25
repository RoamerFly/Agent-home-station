@echo off
setlocal enabledelayedexpansion
rem Switch to this script's directory (app/) so relative paths work.
cd /d "%~dp0"

rem Load environment variables from start.conf (KEY=VALUE, # = comment).
rem findstr keeps only KEY=VALUE lines, so comment lines (which contain
rem multi-byte Chinese text) never reach "for /f" and cannot swallow line breaks.
if exist "start.conf" (
  for /f "usebackq tokens=1,* delims==" %%a in (`findstr /b /r "[A-Za-z_][A-Za-z0-9_]*=" "start.conf"`) do (
    set "%%a=%%b"
    echo %%a=!%%a!
  )
)

rem Run webui.py, forwarding any extra args. (default conda env path)
".\build_venv\python.exe" -u webui.py --inbrowser %*
pause
