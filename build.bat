@echo off
setlocal

REM Build deploy-agent.exe and deploy-agent-gui.exe with UAC-elevating manifests embedded.

set "RSRC=rsrc"

where rsrc >nul 2>nul
if not errorlevel 1 goto :have_rsrc

echo Installing github.com/akavel/rsrc...
go install github.com/akavel/rsrc@latest || goto :error

for /f "delims=" %%G in ('go env GOBIN') do set "GO_TOOL_BIN=%%G"
if not defined GO_TOOL_BIN (
    for /f "delims=" %%G in ('go env GOPATH') do set "GO_TOOL_BIN=%%G\bin"
)
if not defined GO_TOOL_BIN (
    echo Could not determine Go tool bin directory.
    exit /b 1
)

set "RSRC=%GO_TOOL_BIN%\rsrc.exe"
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

echo Building deploy-agent-gui.exe...
set CGO_ENABLED=1
go build -ldflags "-H=windowsgui -s -w" -o deploy-agent-gui.exe .\cmd\deploy-agent-gui || goto :error

echo Done: deploy-agent.exe deploy-agent-gui.exe
exit /b 0

:error
echo Build failed with error %errorlevel%.
exit /b %errorlevel%
