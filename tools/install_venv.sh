#!/bin/bash
# Convenience wrapper to install Python virtual environment for local development
# Usage: ./tools/install_venv.sh [venv_dir]

set -e

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Default venv directory
VENV_DIR="${1:-.venv}"

echo "Installing Python virtual environment..."
echo "Workspace: ${WORKSPACE_ROOT}"
echo "Venv directory: ${VENV_DIR}"
echo

# Run the Python installer script
cd "${WORKSPACE_ROOT}"
python3 "${SCRIPT_DIR}/install_venv.py" --venv-dir "${VENV_DIR}"
