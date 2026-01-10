@echo off
set INSTALL_LOG=atv_pip_install.log
set MY_PATH=%~dp0
cd /d %MY_PATH%

REM Check Python version compatibility (Python 3.9-3.13 required)
REM pyatv uses pydantic V1 which is not compatible with Python 3.14+
for /f "tokens=*" %%i in ('python -c "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')"') do set PYTHON_VERSION=%%i
for /f "tokens=*" %%i in ('python -c "import sys; print(sys.version_info.major)"') do set PYTHON_MAJOR=%%i
for /f "tokens=*" %%i in ('python -c "import sys; print(sys.version_info.minor)"') do set PYTHON_MINOR=%%i

if %PYTHON_MAJOR% LSS 3 (
    echo Error: Python 3.9 or higher is required. Found Python %PYTHON_VERSION% 1>&2
    echo Please install Python 3.9 or higher from https://www.python.org/downloads/ 1>&2
    exit /b 1
)

if %PYTHON_MAJOR% EQU 3 if %PYTHON_MINOR% LSS 9 (
    echo Error: Python 3.9 or higher is required. Found Python %PYTHON_VERSION% 1>&2
    echo Please install Python 3.9 or higher from https://www.python.org/downloads/ 1>&2
    exit /b 1
)

if %PYTHON_MAJOR% GTR 3 (
    echo Error: Python 3.14+ is not supported due to pyatv dependency on pydantic V1. 1>&2
    echo Please use Python 3.9-3.13. Found Python %PYTHON_VERSION% 1>&2
    echo You can install a compatible Python version from https://www.python.org/downloads/ 1>&2
    exit /b 1
)

if %PYTHON_MAJOR% EQU 3 if %PYTHON_MINOR% GEQ 14 (
    echo Error: Python 3.14+ is not supported due to pyatv dependency on pydantic V1. 1>&2
    echo Please use Python 3.9-3.13. Found Python %PYTHON_VERSION% 1>&2
    echo You can install a compatible Python version from https://www.python.org/downloads/ 1>&2
    exit /b 1
)

if not exist env (
    echo ATVRemote - Python install started %DATE% %TIME% >> %INSTALL_LOG%
    echo > setting_up_python
    python -m venv env >> %INSTALL_LOG% 2>&1
    call env\Scripts\activate.bat
    python -m pip install --upgrade pip >> %INSTALL_LOG% 2>&1
    python -m pip install websockets "pyatv>=0.16.1" >> %INSTALL_LOG% 2>&1
    echo ATVRemote - Python install ended %DATE% %TIME% >> %INSTALL_LOG%
    echo ================================================== >> %INSTALL_LOG%
) else (
    call env\Scripts\activate.bat
)

:kill_proc
for /f "tokens=2 delims= " %%A in ('tasklist /FI "IMAGENAME eq python.exe" /NH') do (
    tasklist /FI "WINDOWTITLE eq wsserver.py" | findstr wsserver.py >nul
    if not errorlevel 1 (
        echo Killing %%A
        taskkill /PID %%A /F
    )
)
if exist setting_up_python del setting_up_python
python wsserver.py
