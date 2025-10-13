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

# Check if strip is available
if ! command -v strip &> /dev/null; then
    echo "Warning: 'strip' command not found. Skipping debug symbol stripping."
    echo "Install binutils to enable debug symbol stripping for further size reduction."
    STRIP_AVAILABLE=false
else
    echo "Found 'strip' command. Will strip debug symbols."
    STRIP_AVAILABLE=true
fi

# Find the Python runfiles directory - look for any .runfiles directory
RUNFILES_DIRS=$(find "$PYTHON_DIR" -type d -name "*.runfiles" 2>/dev/null || true)

if [ -z "$RUNFILES_DIRS" ]; then
    echo "Warning: Could not find any runfiles directory in $PYTHON_DIR"
    find "$PYTHON_DIR" -type d | head -20
    exit 0
fi

for RUNFILES_DIR in $RUNFILES_DIRS; do
    echo "Processing runfiles: $RUNFILES_DIR"
    
    # Look for Python installation - only top-level directory, not subdirectories
    PYTHON_INSTALLS=$(find "$RUNFILES_DIR" -maxdepth 1 -type d -name "rules_python++python+python_3_13_*" 2>/dev/null || true)
    
    if [ -z "$PYTHON_INSTALLS" ]; then
        echo "  No Python installation found in this runfiles directory"
        continue
    fi
    
    for PYTHON_INSTALL in $PYTHON_INSTALLS; do
        echo "  Found Python installation: $PYTHON_INSTALL"
        
        # Strip debug symbols from binaries
        if [ "$STRIP_AVAILABLE" = true ] && [ -d "$PYTHON_INSTALL/bin" ]; then
            echo "  Stripping binaries in $PYTHON_INSTALL/bin..."
            find "$PYTHON_INSTALL/bin" -type f -executable -exec strip --strip-debug {} \; 2>/dev/null || true
        fi
        
        # Strip debug symbols from shared libraries
        if [ "$STRIP_AVAILABLE" = true ] && [ -d "$PYTHON_INSTALL/lib" ]; then
            echo "  Stripping shared libraries in $PYTHON_INSTALL/lib..."
            find "$PYTHON_INSTALL/lib" -type f -name "*.so*" -exec strip --strip-debug {} \; 2>/dev/null || true
        fi
        
        # Remove duplicate Python binaries and replace with symlinks
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
done

# Report final size
FINAL_SIZE=$(du -sh "$PYTHON_DIR" 2>/dev/null | cut -f1 || echo "unknown")
echo "Final directory size: $FINAL_SIZE"
