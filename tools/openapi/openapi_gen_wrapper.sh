#!/bin/bash
# Wrapper script to run OpenAPI Generator with Java

set -e

# If first arg is "auto", find Java from system or use fallback
if [ "$1" = "auto" ]; then
    shift  # Remove "auto" from args
    
    # Try to find Java from system first
    if command -v java &> /dev/null; then
        JAVA_RUNTIME="java"
    elif [ -n "$JAVA_HOME" ] && [ -x "$JAVA_HOME/bin/java" ]; then
        JAVA_RUNTIME="$JAVA_HOME/bin/java"
    elif [ -x /usr/bin/java ]; then
        JAVA_RUNTIME="/usr/bin/java"
    else
        # Fallback: next arg should be Bazel-provided Java path
        # This allows: openapi_gen_wrapper.sh auto $(JAVA) ...
        if [ $# -ge 1 ] && [ -x "$1" ]; then
            JAVA_RUNTIME="$1"
            shift
        else
            echo "Error: Java not found. Please install Java, set JAVA_HOME, or provide Java path" >&2
            exit 1
        fi
    fi
else
    JAVA_RUNTIME="$1"
    shift
fi

GENERATOR_JAR="$1"
SPEC_FILE="$2"
OUTPUT_TAR="$3"
PACKAGE_NAME="$4"
NAMESPACE="$5"
APP_NAME="$6"

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

PKG_UNDERSCORE=$(echo "$PACKAGE_NAME" | tr '-' '_')

# Run OpenAPI Generator
"$JAVA_RUNTIME" -jar "$GENERATOR_JAR" generate \
    -i "$SPEC_FILE" \
    -g python \
    -o "$TMPDIR" \
    --package-name "$PKG_UNDERSCORE" \
    --additional-properties=packageName=$PKG_UNDERSCORE,generateSourceCodeOnly=true,library=urllib3

# Fix imports - replace package references with generated.namespace.app
find "$TMPDIR/$PKG_UNDERSCORE" -name "*.py" -type f -exec sed -i \
    -e "s|from $PKG_UNDERSCORE\\.|from generated.$NAMESPACE.$APP_NAME.|g" \
    -e "s|import $PKG_UNDERSCORE\\.|import generated.$NAMESPACE.$APP_NAME.|g" \
    -e "s|from $PKG_UNDERSCORE import|from generated.$NAMESPACE.$APP_NAME import|g" \
    -e "s|^import $PKG_UNDERSCORE\$|import generated.$NAMESPACE.$APP_NAME|g" \
    {} +

# Create tar
tar -cf "$OUTPUT_TAR" -C "$TMPDIR" "$PKG_UNDERSCORE/"
