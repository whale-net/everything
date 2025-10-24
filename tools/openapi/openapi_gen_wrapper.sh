#!/bin/bash
# Wrapper script to run OpenAPI Generator with Java

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
        # This allows: openapi_gen_wrapper.sh auto $(JAVA) ...
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
# Use sed -i '' for macOS compatibility (empty string for no backup), -i for Linux
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    find "$TMPDIR/$PKG_UNDERSCORE" -name "*.py" -type f -exec sed -i '' \
        -e "s|from $PKG_UNDERSCORE\\.|from generated.$NAMESPACE.$APP_NAME.|g" \
        -e "s|import $PKG_UNDERSCORE\\.|import generated.$NAMESPACE.$APP_NAME.|g" \
        -e "s|from $PKG_UNDERSCORE import|from generated.$NAMESPACE.$APP_NAME import|g" \
        -e "s|^import $PKG_UNDERSCORE\$|import generated.$NAMESPACE.$APP_NAME|g" \
        {} +
else
    # Linux
    find "$TMPDIR/$PKG_UNDERSCORE" -name "*.py" -type f -exec sed -i \
        -e "s|from $PKG_UNDERSCORE\\.|from generated.$NAMESPACE.$APP_NAME.|g" \
        -e "s|import $PKG_UNDERSCORE\\.|import generated.$NAMESPACE.$APP_NAME.|g" \
        -e "s|from $PKG_UNDERSCORE import|from generated.$NAMESPACE.$APP_NAME import|g" \
        -e "s|^import $PKG_UNDERSCORE\$|import generated.$NAMESPACE.$APP_NAME|g" \
        {} +
fi

# Fix bug in api_client.py where it references the wrong module name for model deserialization
# The generator creates a reference like "manman_worker_dal_api.models" but the correct import
# is "generated.manman.worker_dal_api.models" (already imported at top of file)
if [ -f "$TMPDIR/$PKG_UNDERSCORE/api_client.py" ]; then
    if [[ "$OSTYPE" == "darwin"* ]]; then
        # macOS
        sed -i '' "s|klass = getattr(${PKG_UNDERSCORE}.models, klass)|import generated.${NAMESPACE}.${APP_NAME}.models\\n                klass = getattr(generated.${NAMESPACE}.${APP_NAME}.models, klass)|g" \
            "$TMPDIR/$PKG_UNDERSCORE/api_client.py"
    else
        # Linux
        sed -i "s|klass = getattr(${PKG_UNDERSCORE}.models, klass)|import generated.${NAMESPACE}.${APP_NAME}.models\\n                klass = getattr(generated.${NAMESPACE}.${APP_NAME}.models, klass)|g" \
            "$TMPDIR/$PKG_UNDERSCORE/api_client.py"
    fi
fi

# Create tar with deterministic timestamp for reproducible builds
tar --mtime='@0' -cf "$OUTPUT_TAR" -C "$TMPDIR" "$PKG_UNDERSCORE/"
