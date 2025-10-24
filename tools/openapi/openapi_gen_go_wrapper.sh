#!/bin/bash
# Wrapper script to run OpenAPI Generator for Go clients

set -e

# If first arg is "auto", find Java from system or use fallback
if [ "$1" = "auto" ]; then
    shift  # Remove "auto" from args
    
    # Try to find Java from system first
    if command -v java &> /dev/null; then
        JAVA_RUNTIME="java"
        shift  # Skip the Bazel Java arg since we're using system Java
    elif [ -n "$JAVA_HOME" ] && [ -x "$JAVA_HOME/bin/java" ]; then
        JAVA_RUNTIME="$JAVA_HOME/bin/java"
        shift  # Skip the Bazel Java arg since we're using JAVA_HOME
    elif [ -x /usr/bin/java ]; then
        JAVA_RUNTIME="/usr/bin/java"
        shift  # Skip the Bazel Java arg since we're using system Java
    else
        # Fallback: next arg should be Bazel-provided Java path
        # This allows: openapi_gen_go_wrapper.sh auto $(JAVA) ...
        JAVA_RUNTIME="$1"
        shift  # Consume the Bazel Java arg
    fi
else
    JAVA_RUNTIME="$1"
    shift
fi

GENERATOR_JAR="$1"
SPEC_FILE="$2"
OUTPUT_TAR="$3"
PACKAGE_NAME="$4"
MODULE_PATH="$5"  # e.g., github.com/whale-net/everything/generated/demo/hello_fastapi

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

# Run OpenAPI Generator for Go
"$JAVA_RUNTIME" -jar "$GENERATOR_JAR" generate \
    -i "$SPEC_FILE" \
    -g go \
    -o "$TMPDIR" \
    --package-name "$PACKAGE_NAME" \
    --git-repo-id "generated" \
    --git-user-id "whale-net" \
    --additional-properties=packageName=$PACKAGE_NAME,enumClassPrefix=true,structPrefix=true

# The generator creates files in TMPDIR root, we want them organized properly
# Structure should be: {PACKAGE_NAME}/*.go

# Ensure all generated files are in the package directory
if [ ! -d "$TMPDIR" ]; then
    echo "Error: Generated output directory not found"
    exit 1
fi

# Create tar with deterministic timestamp for reproducible builds
# Include all go files from the generated output
tar --mtime='@0' -cf "$OUTPUT_TAR" -C "$TMPDIR" .
