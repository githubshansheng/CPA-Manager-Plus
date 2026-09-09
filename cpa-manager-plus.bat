@echo off
setlocal EnableExtensions

set "SCRIPT_DIR=%~dp0"
set "PRODUCTION_DATA_DIR=%SCRIPT_DIR%.local\production-data"
if not defined USAGE_DATA_DIR if exist "%PRODUCTION_DATA_DIR%\usage.sqlite" set "USAGE_DATA_DIR=%PRODUCTION_DATA_DIR%"
if not defined USAGE_DB_PATH if exist "%PRODUCTION_DATA_DIR%\usage.sqlite" set "USAGE_DB_PATH=%PRODUCTION_DATA_DIR%\usage.sqlite"
if not defined CPA_MANAGER_DATA_KEY_PATH if exist "%PRODUCTION_DATA_DIR%\data.key" set "CPA_MANAGER_DATA_KEY_PATH=%PRODUCTION_DATA_DIR%\data.key"

set "START_ACTION=0"
set "FIRST_ARGUMENT=%~1"
if not defined FIRST_ARGUMENT set "START_ACTION=1"
if /I "%FIRST_ARGUMENT%"=="start" set "START_ACTION=1"
if /I "%FIRST_ARGUMENT%"=="restart" set "START_ACTION=1"
if /I "%FIRST_ARGUMENT%"=="rebuild" set "START_ACTION=1"
if /I "%FIRST_ARGUMENT%"=="--port" set "START_ACTION=1"
if /I "%FIRST_ARGUMENT:~0,7%"=="--port=" set "START_ACTION=1"

if "%START_ACTION%"=="1" call :prepare_startup_log

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_DIR%cpa-manager-plus.ps1" %*
set "EXIT_CODE=%ERRORLEVEL%"

if not "%START_ACTION%"=="1" exit /b %EXIT_CODE%
if "%EXIT_CODE%"=="0" goto startup_success

echo.
echo [ERROR] CPA Manager Plus failed to start with exit code %EXIT_CODE%.
echo [INFO] Review the startup log: %CPA_MANAGER_PLUS_STARTUP_LOG%
if /I not "%CPA_MANAGER_PLUS_NO_PAUSE%"=="1" (
    echo.
    echo This window will remain open so the error can be reviewed.
    pause
)
exit /b %EXIT_CODE%

:startup_success
echo [INFO] Startup completed successfully. Closing this window.
exit /b 0

:prepare_startup_log
if not defined CPA_MANAGER_PLUS_STARTUP_LOG set "CPA_MANAGER_PLUS_STARTUP_LOG=%SCRIPT_DIR%.local\control\logs\startup.log"
for %%I in ("%CPA_MANAGER_PLUS_STARTUP_LOG%") do set "STARTUP_LOG_DIR=%%~dpI"
if not exist "%STARTUP_LOG_DIR%" mkdir "%STARTUP_LOG_DIR%" >nul 2>&1
echo [INFO] Starting CPA Manager Plus...
echo [INFO] Startup log: %CPA_MANAGER_PLUS_STARTUP_LOG%
exit /b 0
