#!/bin/bash
# Wrapper script to run OpenAPI Generator with Java

set -e

JAVA_RUNTIME="$1"
GENERATOR_JAR="$2"
SPEC_FILE="$3"
OUTPUT_TAR="$4"
PACKAGE_NAME="$5"
NAMESPACE="$6"
APP_NAME="$7"

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

# Fix imports
find "$TMPDIR/$PKG_UNDERSCORE" -name "*.py" -type f -exec sed -i \
    "s|from $PKG_UNDERSCORE\\.|from external.$NAMESPACE.$APP_NAME.|g" {} +
find "$TMPDIR/$PKG_UNDERSCORE" -name "*.py" -type f -exec sed -i \
    "s|import $PKG_UNDERSCORE\\.|import external.$NAMESPACE.$APP_NAME.|g" {} +
find "$TMPDIR/$PKG_UNDERSCORE" -name "*.py" -type f -exec sed -i \
    "s|from $PKG_UNDERSCORE import|from external.$NAMESPACE.$APP_NAME import|g" {} +
find "$TMPDIR/$PKG_UNDERSCORE" -name "*.py" -type f -exec sed -i \
    "s|^import $PKG_UNDERSCORE\$|import external.$NAMESPACE.$APP_NAME|g" {} +

# Create tar
tar -cf "$OUTPUT_TAR" -C "$TMPDIR" "$PKG_UNDERSCORE/"
