#!/bin/bash
# Convenience wrapper to install Python virtual environment for local development
# Usage: ./tools/install_venv.sh [venv_dir]
# Or with options: ./tools/install_venv.sh --venv-dir my_venv

set -e

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Change to workspace root
cd "${WORKSPACE_ROOT}"

# If first argument doesn't start with --, treat it as venv_dir for backwards compat
if [ $# -eq 0 ]; then
    # No arguments, use default
    python3 "${SCRIPT_DIR}/install_venv.py"
elif [ "${1:0:2}" == "--" ]; then
    # Arguments start with --, pass them through
    python3 "${SCRIPT_DIR}/install_venv.py" "$@"
else
    # First argument is venv directory
    python3 "${SCRIPT_DIR}/install_venv.py" --venv-dir "$1"
fi
