@echo off
setlocal

REM Build deploy-agent.exe with UAC-elevating manifest embedded.

where rsrc >nul 2>nul
if errorlevel 1 (
    echo Installing github.com/akavel/rsrc...
    go install github.com/akavel/rsrc@latest || goto :error
)

echo Embedding manifest...
rsrc -manifest deploy-agent.manifest -o resource.syso || goto :error

echo Building...
set GOOS=windows
set CGO_ENABLED=0
go build -ldflags "-s -w" -o deploy-agent.exe . || goto :error

echo Done: deploy-agent.exe
exit /b 0

:error
echo Build failed with error %errorlevel%.
exit /b %errorlevel%
