@echo off
setlocal
cd /d "%~dp0"
title MultiViewer Replay Sync - Setup

echo MultiViewer Replay Sync - setup
echo.

node -e "process.exit(+process.versions.node.split('.')[0] >= 20 ? 0 : 1)" >nul 2>nul
if not errorlevel 1 goto deps

echo Node.js 20 or newer is needed and was not found.
where winget >nul 2>nul
if errorlevel 1 (
  echo Please install the LTS version from https://nodejs.org and run this again.
  pause
  exit /b 1
)
choice /m "Install Node.js LTS now with winget"
if errorlevel 2 (
  echo Cancelled. You can also install it from https://nodejs.org
  pause
  exit /b 1
)
winget install -e --id OpenJS.NodeJS.LTS
rem make the fresh install visible to this window
set "PATH=%ProgramFiles%\nodejs;%PATH%"
node -v >nul 2>nul
if errorlevel 1 (
  echo Node.js was installed but isn't visible yet. Close this window and run install.bat again.
  pause
  exit /b 1
)

:deps
echo Installing dependencies...
call npm install --omit=dev --no-audit --no-fund
if errorlevel 1 (
  echo Install failed.
  pause
  exit /b 1
)

echo.
echo Hosting? Friends can only connect if Windows Firewall lets them in.
choice /m "Allow port 8765 on ZeroTier networks now (asks for admin rights)"
if errorlevel 2 goto done
powershell -NoProfile -Command "try { $p = Start-Process powershell -Verb RunAs -Wait -PassThru -ArgumentList '-NoProfile -Command New-NetFirewallRule -DisplayName ''MultiViewer Replay Sync'' -Direction Inbound -Protocol TCP -LocalPort 8765 -Action Allow -InterfaceAlias ''ZeroTier*'''; exit $p.ExitCode } catch { exit 1 }"
if errorlevel 1 (
  echo Could not add the firewall rule. See the README for doing it by hand.
) else (
  echo Firewall rule added.
)

:done
echo.
echo All set. Start the app with start.bat
pause
