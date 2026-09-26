@echo off
rem WebView2 white-screen diagnostic. Run from a normal cmd window.
rem 诊断工具 cmd/echotest（源码），产物 build\echotest.exe；结果写入 build\wv2_diag.txt
cd /d "%~dp0"
echo ==== WebView2 diagnostic ==== > ..\build\wv2_diag.txt
echo time: %date% %time% >> ..\build\wv2_diag.txt
echo. >> ..\build\wv2_diag.txt

echo [1/3] variant none (no extra args, baseline)...
echo ==== [1/3] none ==== >> ..\build\wv2_diag.txt
..\build\echotest.exe none >> ..\build\wv2_diag.txt 2>&1
echo. >> ..\build\wv2_diag.txt

echo [2/3] variant software (disable-gpu-driver-bug-workarounds ignore-gpu-blocklist)...
echo ==== [2/3] software ==== >> ..\build\wv2_diag.txt
..\build\echotest.exe software >> ..\build\wv2_diag.txt 2>&1
echo. >> ..\build\wv2_diag.txt

echo [3/3] variant nosandbox (--no-sandbox, expected FIX)...
echo ==== [3/3] nosandbox ==== >> ..\build\wv2_diag.txt
..\build\echotest.exe nosandbox >> ..\build\wv2_diag.txt 2>&1
echo. >> ..\build\wv2_diag.txt

echo.
echo Done. Results saved to build\wv2_diag.txt
pause
