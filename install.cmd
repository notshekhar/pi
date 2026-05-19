@echo off
:: pi installer (Windows CMD) — bootstraps PowerShell installer.
::   curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.cmd -o install.cmd ^& install.cmd ^& del install.cmd

setlocal
set "PS1_URL=https://raw.githubusercontent.com/notshekhar/pi/main/install.ps1"

where powershell >nul 2>&1
if errorlevel 1 (
  echo PowerShell not found. Install PowerShell or Windows 10+ to use this installer.
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -Command "irm %PS1_URL% | iex"
exit /b %ERRORLEVEL%
