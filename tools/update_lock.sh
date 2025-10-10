#!/bin/bash
# Update uv.lock after modifying pyproject.toml dependencies
# This script requires uv to be installed: https://github.com/astral-sh/uv

set -euo pipefail

echo "Updating uv.lock file with new dependencies..."

# Check if uv is installed
if ! command -v uv &> /dev/null; then
    echo "Error: uv is not installed"
    echo "Install with: curl -LsSf https://astral.sh/uv/install.sh | sh"
    echo "Or see: https://github.com/astral-sh/uv"
    exit 1
fi

# Update the lock file
echo "Running: uv lock --python 3.11"
uv lock --python 3.11

echo ""
echo "✅ Lock file updated successfully!"
echo ""
echo "Changes have been made to uv.lock. Please review and commit:"
echo "  git add uv.lock"
echo "  git commit -m 'Update uv.lock with new dependencies'"
