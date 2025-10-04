#!/bin/bash
# Create Python virtual environment using requirements.lock.txt
# Simple shell script alternative to Python tool approach

set -euo pipefail

# Parse command line arguments
VENV_PATH=".venv"
PYTHON_EXECUTABLE="python3"

# Simple argument parsing
while [[ $# -gt 0 ]]; do
    case $1 in
        --venv-path)
            VENV_PATH="$2"
            shift 2
            ;;
        --python)
            PYTHON_EXECUTABLE="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [--venv-path PATH] [--python PYTHON_CMD]"
            echo ""
            echo "Create a Python virtual environment using requirements.lock.txt"
            echo ""
            echo "Options:"
            echo "  --venv-path PATH     Path where venv should be created (default: .venv)"
            echo "  --python PYTHON_CMD  Python executable to use (default: python3)"
            echo "  --help              Show this help message"
            echo ""
            echo "Examples:"
            echo "  bazel run //tools:create_venv"
            echo "  bazel run //tools:create_venv -- --venv-path ./my_venv"
            echo "  bazel run //tools:create_venv -- --python python3.11"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Get workspace root
WORKSPACE_ROOT="${BUILD_WORKSPACE_DIRECTORY:-$(pwd)}"
REQUIREMENTS_FILE="${WORKSPACE_ROOT}/requirements.lock.txt"

# Helper function to print messages
log() {
    echo "🔧 $1"
}

# Verify requirements file exists
if [[ ! -f "$REQUIREMENTS_FILE" ]]; then
    echo "❌ Error: Requirements file not found: $REQUIREMENTS_FILE"
    exit 1
fi

log "Creating Python virtual environment for local development"
log "Workspace: $WORKSPACE_ROOT"
log "Virtual environment path: $VENV_PATH"
log "Requirements file: $REQUIREMENTS_FILE"

# Make venv path absolute if relative
if [[ "$VENV_PATH" != /* ]]; then
    VENV_PATH="$WORKSPACE_ROOT/$VENV_PATH"
fi

# Create virtual environment
log "Creating virtual environment at: $VENV_PATH"
"$PYTHON_EXECUTABLE" -m venv "$VENV_PATH" --clear

# Determine activation script and pip paths
if [[ "$OSTYPE" == "msys" || "$OSTYPE" == "cygwin" ]]; then
    # Windows paths
    ACTIVATE_SCRIPT="$VENV_PATH/Scripts/activate"
    PIP_EXECUTABLE="$VENV_PATH/Scripts/pip"
    PYTHON_VENV="$VENV_PATH/Scripts/python"
else
    # Unix paths
    ACTIVATE_SCRIPT="$VENV_PATH/bin/activate"
    PIP_EXECUTABLE="$VENV_PATH/bin/pip"
    PYTHON_VENV="$VENV_PATH/bin/python"
fi

# Upgrade pip first
log "Upgrading pip..."
"$PYTHON_VENV" -m pip install --upgrade pip

# Install requirements
log "Installing requirements from: $REQUIREMENTS_FILE"
"$PIP_EXECUTABLE" install -r "$REQUIREMENTS_FILE"

log "✅ Virtual environment created successfully!"
log "📁 Location: $VENV_PATH"
log ""
log "🚀 To activate the environment:"
if [[ "$OSTYPE" == "msys" || "$OSTYPE" == "cygwin" ]]; then
    log "   $VENV_PATH\\Scripts\\activate"
else
    log "   source $ACTIVATE_SCRIPT"
fi
log ""
log "📦 Installed packages from: $REQUIREMENTS_FILE"