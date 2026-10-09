@echo off
setlocal
cd /d "%~dp0"
title MultiViewer Replay Sync

node -e "process.exit(+process.versions.node.split('.')[0] >= 20 ? 0 : 1)" >nul 2>nul
if errorlevel 1 (
  echo Node.js 20 or newer is required. Run install.bat first.
  echo.
  pause
  exit /b 1
)

rem (Re)install dependencies when they're missing or out of date, e.g. after an update
call npm ls --omit=dev >nul 2>nul
if errorlevel 1 (
  echo Installing dependencies...
  call npm install --omit=dev --no-audit --no-fund
  if errorlevel 1 (
    echo Install failed.
    pause
    exit /b 1
  )
  echo.
)

node src\cli.js %*
echo.
pause
