@echo off
rem 讀取 branding.jsonc，產生 dist\<exeName> 與 dist\latest.json
rem 用法：build.cmd            （預設 amd64）
rem       build.cmd -arch arm64
setlocal
cd /d "%~dp0"
go run ./tools/build %*
