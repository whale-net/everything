#!/bin/bash
# Wrapper for bazel-diff JAR

set -euo pipefail

# Find the JAR file - it's in the data dependencies
RUNFILES="${BASH_SOURCE[0]}.runfiles"
if [[ ! -d "$RUNFILES" ]]; then
    RUNFILES="${RUNFILES%/*}"
fi

JAR_PATH="$RUNFILES/_main/external/bazel_diff/file/bazel-diff.jar"

if [[ ! -f "$JAR_PATH" ]]; then
    echo "Error: bazel-diff.jar not found at $JAR_PATH" >&2
    exit 1
fi

# Run bazel-diff with all arguments
exec java -jar "$JAR_PATH" "$@"
