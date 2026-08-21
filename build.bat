@echo off
setlocal

REM Build deploy-agent-gui.exe as the primary GUI+service app, plus deploy-agent.exe as a compatible console service.

set "RSRC=rsrc"

where rsrc >nul 2>nul
if not errorlevel 1 goto :have_rsrc

call :set_go_tool_bin || goto :error
set "RSRC=%GO_TOOL_BIN%\rsrc.exe"
if exist "%RSRC%" goto :have_rsrc

echo Installing github.com/akavel/rsrc...
go install github.com/akavel/rsrc@latest || goto :error
if not exist "%RSRC%" (
    echo Could not find rsrc.exe at "%RSRC%".
    exit /b 1
)

:have_rsrc
echo Embedding deploy-agent manifest...
"%RSRC%" -manifest deploy-agent.manifest -o resource.syso || goto :error

echo Building deploy-agent.exe...
set GOOS=windows
set CGO_ENABLED=0
go build -ldflags "-s -w" -o deploy-agent.exe . || goto :error

echo Embedding deploy-agent-gui manifest...
"%RSRC%" -manifest deploy-agent.manifest -o cmd\deploy-agent-gui\resource.syso || goto :error

echo Checking C compiler for deploy-agent-gui.exe...
call :ensure_gcc || goto :error

echo Building deploy-agent-gui.exe...
set CGO_ENABLED=1
go build -ldflags "-H=windowsgui -s -w" -o deploy-agent-gui.exe .\cmd\deploy-agent-gui || goto :error

echo Done: deploy-agent-gui.exe deploy-agent.exe
exit /b 0

:set_go_tool_bin
for /f "delims=" %%G in ('go env GOBIN') do set "GO_TOOL_BIN=%%G"
if not defined GO_TOOL_BIN (
    for /f "delims=" %%G in ('go env GOPATH') do set "GO_TOOL_BIN=%%G\bin"
)
if not defined GO_TOOL_BIN (
    echo Could not determine Go tool bin directory.
    exit /b 1
)
exit /b 0

:ensure_gcc
where gcc >nul 2>nul
if not errorlevel 1 exit /b 0

where x86_64-w64-mingw32-gcc >nul 2>nul
if not errorlevel 1 (
    set "CC=x86_64-w64-mingw32-gcc"
    exit /b 0
)

call :add_gcc_bin "%LOCALAPPDATA%\Programs\WinLibs\gcc-16.1.0-ucrt-posix\mingw64\bin"
if not errorlevel 1 exit /b 0

call :add_gcc_bin "%LOCALAPPDATA%\Programs\WinLibs\mingw64\bin"
if not errorlevel 1 exit /b 0

for /d %%D in ("%LOCALAPPDATA%\Programs\WinLibs\*\mingw64\bin") do (
    if exist "%%~fD\gcc.exe" (
        set "PATH=%%~fD;%PATH%"
        echo Found gcc at "%%~fD".
        exit /b 0
    )
)

call :add_gcc_bin "C:\msys64\ucrt64\bin"
if not errorlevel 1 exit /b 0

call :add_gcc_bin "C:\msys64\mingw64\bin"
if not errorlevel 1 exit /b 0

call :add_gcc_bin "C:\mingw64\bin"
if not errorlevel 1 exit /b 0

echo Could not find gcc, which is required to build deploy-agent-gui.exe.
echo Install MinGW-w64 and make sure its mingw64\bin directory is in PATH.
echo Tested option: WinLibs POSIX UCRT under %%LOCALAPPDATA%%\Programs\WinLibs.
exit /b 1

:add_gcc_bin
if "%~1"=="" exit /b 1
if exist "%~1\gcc.exe" (
    set "PATH=%~1;%PATH%"
    echo Found gcc at "%~1".
    exit /b 0
)
exit /b 1

:error
echo Build failed with error %errorlevel%.
exit /b %errorlevel%
