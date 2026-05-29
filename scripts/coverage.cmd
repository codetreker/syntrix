@echo off
setlocal

cd /d "%~dp0.."
go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.0 --skip-result-packages tests/ %*
set EXITCODE=%ERRORLEVEL%

endlocal & exit /b %EXITCODE%
