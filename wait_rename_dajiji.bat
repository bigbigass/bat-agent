@echo off
setlocal EnableExtensions EnableDelayedExpansion

rem Usage as preRun.download.script:
rem   wait_rename_dajiji.bat <project> <artifact>
rem Usage from cmd:
rem   wait_rename_dajiji.bat <artifact>

set "PROJECT=%~1"
set "ARTIFACT=%~2"
if "%ARTIFACT%"=="" (
  set "ARTIFACT=%PROJECT%"
  set "PROJECT="
)
set "TARGET=dajiji"
set "MAX_WAIT_SECONDS=300"

if "%ARTIFACT%"=="" (
  echo no artifact specified; nothing to do
  exit /b 0
)

for %%F in ("%ARTIFACT%") do set "ARTIFACT_NAME=%%~nxF"
if not "%ARTIFACT%"=="%ARTIFACT_NAME%" (
  echo artifact must be a file name only: %ARTIFACT%
  exit /b 2
)

if not "%PROJECT%"=="" (
  for %%F in ("%PROJECT%") do set "PROJECT_NAME=%%~nxF"
  if not "%PROJECT%"=="!PROJECT_NAME!" (
    echo project must be a folder name only: %PROJECT%
    exit /b 2
  )
)

if /I "%ARTIFACT%"=="%TARGET%" (
  if exist "%TARGET%" (
    echo artifact is already named %TARGET%
    exit /b 0
  )
)

echo waiting for artifact: %ARTIFACT%
if not "%PROJECT%"=="" echo project: %PROJECT%

set "LAST_SIZE="
set "STABLE_TICKS=0"
set "FOUND_PATH="

for /L %%S in (1,1,%MAX_WAIT_SECONDS%) do (
  set "FOUND_PATH="
  if not "%PROJECT%"=="" (
    if exist "tools\download\%PROJECT%\%ARTIFACT%" set "FOUND_PATH=tools\download\%PROJECT%\%ARTIFACT%"
  )
  if not defined FOUND_PATH (
    if exist "%ARTIFACT%" set "FOUND_PATH=%ARTIFACT%"
  )

  if defined FOUND_PATH (
    for %%F in ("!FOUND_PATH!") do set "CURRENT_SIZE=%%~zF"
    if "!CURRENT_SIZE!"=="!LAST_SIZE!" (
      set /A STABLE_TICKS+=1
    ) else (
      set "LAST_SIZE=!CURRENT_SIZE!"
      set "STABLE_TICKS=0"
    )

    if !STABLE_TICKS! GEQ 2 goto rename_artifact
  )
  ping -n 2 127.0.0.1 >nul
)

echo timed out waiting for %ARTIFACT%
exit /b 1

:rename_artifact
for %%F in ("!FOUND_PATH!") do (
  set "FOUND_DIR=%%~dpF"
  set "FOUND_NAME=%%~nxF"
)
set "TARGET_PATH=!FOUND_DIR!%TARGET%"

if exist "!TARGET_PATH!" (
  echo target already exists: !TARGET_PATH!
  exit /b 1
)

ren "!FOUND_PATH!" "%TARGET%"
if errorlevel 1 (
  echo failed to rename !FOUND_PATH! to %TARGET%
  exit /b 1
)

echo renamed !FOUND_PATH! to !TARGET_PATH!
exit /b 0
