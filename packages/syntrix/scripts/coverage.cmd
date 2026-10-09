@echo off
setlocal

cd /d "%~dp0.."
if errorlevel 1 exit /b %ERRORLEVEL%
go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0 --coverprofile=coverage.out --skip-result-packages tests/ %*
set EXITCODE=%ERRORLEVEL%

endlocal & exit /b %EXITCODE%
