@echo off
setlocal
set "RF4_PYTHON=%LocalAppData%\Programs\Python\Python312\pythonw.exe"
if not exist "%RF4_PYTHON%" (
  echo Python 3.12 was not found. Run local_server.py with Python 3.12 or newer.
  pause
  exit /b 1
)
start "RF4 Local Service" "%RF4_PYTHON%" "%~dp0local_server.py" --managed-backend --open-browser
endlocal
