#!/bin/bash
# Script to run OpenAPI generator with host Java (exec platform)
# This script ensures we use the build host's Java, not the target platform's

set -e

# Find Java - try common locations
if command -v java &> /dev/null; then
    JAVA_BIN="java"
elif [ -n "$JAVA_HOME" ] && [ -x "$JAVA_HOME/bin/java" ]; then
    JAVA_BIN="$JAVA_HOME/bin/java"
elif [ -x /usr/bin/java ]; then
    JAVA_BIN="/usr/bin/java"
else
    echo "Error: Java not found. Please install Java or set JAVA_HOME" >&2
    exit 1
fi

# Forward all arguments to the wrapper
exec "$(dirname "$0")/openapi_gen_wrapper.sh" "$JAVA_BIN" "$@"
