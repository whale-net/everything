#!/bin/bash
# Strip debug symbols and remove duplicate Python binaries to reduce image size
#
# This script:
# 1. Strips debug symbols from Python binaries and shared libraries (~200-300MB savings)
# 2. Removes duplicate python/python3 binaries, keeping only python3.13 (~200MB savings)
# 3. Creates symlinks for python -> python3.13 and python3 -> python3.13
#
# Safe to run in production - only affects binary size, not functionality.
# Python stack traces, debugging, and all normal operations still work.

set -euo pipefail

PYTHON_DIR="$1"

if [ ! -d "$PYTHON_DIR" ]; then
    echo "Error: Directory $PYTHON_DIR does not exist"
    exit 1
fi

echo "Optimizing Python installation in $PYTHON_DIR"

# Find Python installations - either in runfiles or directly
PYTHON_INSTALLS=""

# First try: look for .runfiles directories (standard Bazel structure)
RUNFILES_DIRS=$(find "$PYTHON_DIR" -type d -name "*.runfiles" 2>/dev/null || true)
if [ -n "$RUNFILES_DIRS" ]; then
    for RUNFILES_DIR in $RUNFILES_DIRS; do
        echo "Processing runfiles: $RUNFILES_DIR"
        # Look for Python installation - only top-level directory, not subdirectories
        FOUND=$(find "$RUNFILES_DIR" -maxdepth 1 -type d -name "rules_python++python+python_3_13_*" 2>/dev/null || true)
        if [ -n "$FOUND" ]; then
            PYTHON_INSTALLS="$PYTHON_INSTALLS $FOUND"
        fi
    done
fi

# Second try: look for direct Python installation (e.g., /opt/python3.13/x86_64-unknown-linux-gnu/)
if [ -z "$PYTHON_INSTALLS" ]; then
    # Check if this directory itself is a Python installation (has bin/ and lib/ subdirs)
    if [ -d "$PYTHON_DIR/bin" ] && [ -d "$PYTHON_DIR/lib" ] && [ -f "$PYTHON_DIR/bin/python3.13" ]; then
        echo "Found direct Python installation: $PYTHON_DIR"
        PYTHON_INSTALLS="$PYTHON_DIR"
    fi
fi

if [ -z "$PYTHON_INSTALLS" ]; then
    echo "Warning: Could not find any Python installation in $PYTHON_DIR"
    find "$PYTHON_DIR" -type d | head -20
    exit 0
fi

for PYTHON_INSTALL in $PYTHON_INSTALLS; do
    echo "  Optimizing: $PYTHON_INSTALL"
    
    # Note: We DO NOT strip the Python binary itself as it breaks execution
    # The python3.13 binary is ~108MB with debug symbols, which is acceptable
    # Stripping would save ~80MB but breaks the binary
    
    # Strip shared libraries only (saves ~100-150MB)
    if [ -d "$PYTHON_INSTALL/lib" ]; then
        echo "  Stripping shared libraries in $PYTHON_INSTALL/lib..."
        # Make files writable before stripping
        find "$PYTHON_INSTALL/lib" -type f -name "*.so*" -exec chmod +w {} \; 2>/dev/null || true
        find "$PYTHON_INSTALL/lib" -type f -name "*.so*" -exec strip --strip-unneeded {} \; 2>/dev/null || true
    fi
    
    # Remove duplicate Python binaries and replace with symlinks (saves ~216MB)
    if [ -f "$PYTHON_INSTALL/bin/python3.13" ]; then
        echo "  Removing duplicate python binaries and creating symlinks..."
        
        # Get sizes before
        if [ -f "$PYTHON_INSTALL/bin/python" ]; then
            BEFORE_SIZE=$(du -sh "$PYTHON_INSTALL/bin/python" 2>/dev/null | cut -f1 || echo "unknown")
            echo "    Before: python=$BEFORE_SIZE"
        fi
        
        # Remove duplicates if they're actual files (not already symlinks)
        if [ -f "$PYTHON_INSTALL/bin/python" ] && [ ! -L "$PYTHON_INSTALL/bin/python" ]; then
            rm -f "$PYTHON_INSTALL/bin/python"
            ln -s python3.13 "$PYTHON_INSTALL/bin/python"
            echo "    Created symlink: python -> python3.13"
        fi
        
        if [ -f "$PYTHON_INSTALL/bin/python3" ] && [ ! -L "$PYTHON_INSTALL/bin/python3" ]; then
            rm -f "$PYTHON_INSTALL/bin/python3"
            ln -s python3.13 "$PYTHON_INSTALL/bin/python3"
            echo "    Created symlink: python3 -> python3.13"
        fi
    fi
done

# Report final size
FINAL_SIZE=$(du -sh "$PYTHON_DIR" 2>/dev/null | cut -f1 || echo "unknown")
echo "Final directory size: $FINAL_SIZE"
