#!/bin/bash
INSTALL_LOG="atv_pip_install.log"
MY_PATH=$(dirname "$0")
cd "$MY_PATH"

# Check Python version compatibility (Python 3.9-3.13 required)
# pyatv uses pydantic V1 which is not compatible with Python 3.14+
PYTHON_VERSION=$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')
PYTHON_MAJOR=$(python3 -c 'import sys; print(sys.version_info.major)')
PYTHON_MINOR=$(python3 -c 'import sys; print(sys.version_info.minor)')

if [[ $PYTHON_MAJOR -lt 3 ]] || [[ $PYTHON_MAJOR -eq 3 && $PYTHON_MINOR -lt 9 ]]; then
	echo "Error: Python 3.9 or higher is required. Found Python $PYTHON_VERSION" >&2
	echo "Please install Python 3.9 or higher from https://www.python.org/downloads/" >&2
	exit 1
fi

if [[ $PYTHON_MAJOR -gt 3 ]] || [[ $PYTHON_MAJOR -eq 3 && $PYTHON_MINOR -ge 14 ]]; then
	echo "Error: Python 3.14+ is not supported due to pyatv dependency on pydantic V1." >&2
	echo "Please use Python 3.9-3.13. Found Python $PYTHON_VERSION" >&2
	echo "You can install a compatible Python version from https://www.python.org/downloads/" >&2
	exit 1
fi

ENV_DIR="env"
if [[ $(uname -m) == "i386" ]]; then
	ENV_DIR="env_x86"
fi
if [[ ! -d $ENV_DIR ]]; then
	dt=$(date)
	echo "ATVRemote - Python install started $dt" >> $INSTALL_LOG
	touch setting_up_python
	python3 -m venv $ENV_DIR | tee -a $INSTALL_LOG
	source $ENV_DIR/bin/activate
	python -m pip install --upgrade pip | tee -a $INSTALL_LOG
	python -m pip install "pyatv>=0.16.1" | tee -a $INSTALL_LOG
	dt=$(date)
	echo "ATVRemote - Python install ended $dt" >> $INSTALL_LOG
	echo "==================================================" >> $INSTALL_LOG
else
	source $ENV_DIR/bin/activate
fi

function kill_proc () {
	for p in $(ps ax | grep -v grep | grep -E 'wsserver$|wsserver.exe' | awk '{print $1}'); do
		echo "Killing $p"
		kill $1 $p
	done
}
kill_proc
kill_proc "-9"
[[ -f setting_up_python ]] && rm setting_up_python

# Build Go server if needed
if [[ ! -f wsserver ]] || [[ wsserver.go -nt wsserver ]]; then
	echo "Building Go WebSocket server..."
	if command -v go &> /dev/null; then
		go build -o wsserver wsserver.go
	elif [[ -f wsserver ]]; then
		echo "Go not found, using existing binary"
	else
		echo "Error: Go is not installed and no pre-built binary exists" >&2
		exit 1
	fi
fi

./wsserver
