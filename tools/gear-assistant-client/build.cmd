@echo off
setlocal
where cl >nul 2>nul
if errorlevel 1 exit /b 1
cd /d "%~dp0"
if not exist out\temp mkdir out\temp
set "TEMP=%~dp0out\temp"
set "TMP=%TEMP%"
powershell -NoProfile -ExecutionPolicy Bypass -File make-exports.ps1
if errorlevel 1 exit /b 1
lib /nologo /DEF:out\system.def /MACHINE:X64 /OUT:out\system.lib
if errorlevel 1 exit /b 1
cl /nologo /LD /O2 /MT /EHsc /W4 /std:c++17 /utf-8 client.cpp /Foout\client.obj /Feout\winmm.dll /link /DEF:out\winmm.def /IMPLIB:out\client.lib out\system.lib user32.lib
if errorlevel 1 exit /b 1
copy /y "%SystemRoot%\System32\winmm.dll" "out\rf4_gear_winmm_system.dll" >nul
exit /b %errorlevel%
