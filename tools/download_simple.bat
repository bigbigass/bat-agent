@echo off
setlocal EnableExtensions DisableDelayedExpansion
chcp 65001 >nul
set "PROJECT_NAME=%~1"
set "ARTIFACT_NAME=%~2"
if "%PROJECT_NAME%"=="" goto :usage
if "%ARTIFACT_NAME%"=="" goto :usage

for %%I in ("%~f0") do set "SCRIPT_DIR=%%~dpI"
cd /d "%SCRIPT_DIR%"

if not exist "%SCRIPT_DIR%.config" mkdir "%SCRIPT_DIR%.config" >nul 2>nul

set "BAIDUPCS_GO_CONFIG_DIR=%SCRIPT_DIR%.config"
set "BPCS_EXE=%SCRIPT_DIR%BaiduPCS-Go.exe"
set "COOKIE_CONFIG=%SCRIPT_DIR%cookie.ini"

if not exist "%BPCS_EXE%" (
  echo BaiduPCS-Go.exe was not found in:
  echo %SCRIPT_DIR%
  pause
  exit /b 1
)

if not exist "%COOKIE_CONFIG%" (
  echo cookie.ini was not found in:
  echo %SCRIPT_DIR%
  pause
  exit /b 1
)

set "BPCS_COOKIES_B64="
for /f "usebackq tokens=1* delims==" %%A in (`findstr /b /i /c:"cookie_b64=" "%COOKIE_CONFIG%"`) do (
  if /i "%%A"=="cookie_b64" set "BPCS_COOKIES_B64=%%B"
)

set "BPCS_COOKIES="
if not defined BPCS_COOKIES_B64 (
  for /f "usebackq tokens=1* delims==" %%A in (`findstr /b /i /c:"cookie=" "%COOKIE_CONFIG%"`) do (
    if /i "%%A"=="cookie" set "BPCS_COOKIES=%%B"
  )
)

if defined BPCS_COOKIES_B64 (
  for /f "usebackq delims=" %%I in (`powershell -NoProfile -Command "[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($env:BPCS_COOKIES_B64))"`) do set "BPCS_COOKIES=%%I"
)

if not defined BPCS_COOKIES (
  echo cookie.ini does not contain a valid cookie_b64= or cookie= entry.
  pause
  exit /b 1
)

echo Logging in...
"%BPCS_EXE%" login -cookies="%BPCS_COOKIES%"
set "BPCS_COOKIES="
set "BPCS_COOKIES_B64="

if errorlevel 1 (
  echo Login failed. The cookie may have expired.
  pause
  exit /b 1
)

setlocal EnableDelayedExpansion
set "REMOTE_PATH=/交付产物/!PROJECT_NAME!/!ARTIFACT_NAME!"
set "DOWNLOAD_ROOT=%SCRIPT_DIR%download\!PROJECT_NAME!"

if not exist "!DOWNLOAD_ROOT!" mkdir "!DOWNLOAD_ROOT!" >nul 2>nul

echo.
echo Checking remote artifact: !REMOTE_PATH!
"%BPCS_EXE%" meta "!REMOTE_PATH!" >nul 2>nul
if errorlevel 1 (
  echo Remote artifact was not found:
  echo !REMOTE_PATH!
  pause
  exit /b 1
)

echo Downloading...
"%BPCS_EXE%" download --saveto "!DOWNLOAD_ROOT!" "!REMOTE_PATH!"
if errorlevel 1 (
  echo Download failed.
  pause
  exit /b 1
)

echo Download completed:
echo !DOWNLOAD_ROOT!\!ARTIFACT_NAME!
pause
exit /b 0

:usage
echo Usage: download_simple.bat ^<project_name^> ^<artifact_name^>
echo Example: download_simple.bat ProjectA app.zip
pause
exit /b 1
