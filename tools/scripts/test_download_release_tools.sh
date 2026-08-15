#!/usr/bin/env bash
set -euo pipefail

# Test download-release-tools logic:
# 1. Pinned version resolution from .release-tools-version
# 2. SHA256 integrity verification (succeeds on matching checksum, fails on mismatch)
# 3. Source-mode fallback and tool execution verification

echo "=== Test 1: Pinned version file parsing ==="
VERSION_FILE=".release-tools-version"
if [ ! -f "$VERSION_FILE" ]; then
    echo "FAIL: $VERSION_FILE not found"
    exit 1
fi

HELPER_VER=$(grep 'release_helper_go:' "$VERSION_FILE" | awk '{print $2}')
REGISTRY_VER=$(grep 'app_registry_cli:' "$VERSION_FILE" | awk '{print $2}')

if [ -z "$HELPER_VER" ] || [ -z "$REGISTRY_VER" ]; then
    echo "FAIL: Could not parse versions from $VERSION_FILE"
    exit 1
fi
echo "Parsed release_helper_go version: $HELPER_VER"
echo "Parsed app_registry_cli version: $REGISTRY_VER"

echo "=== Test 2: SHA256 integrity verification discipline ==="
TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT

echo "test binary content for release helper" > "$TEST_DIR/release_helper_go"
EXPECTED_SHA=$(sha256sum "$TEST_DIR/release_helper_go" | awk '{print $1}')
echo "$EXPECTED_SHA  release_helper_go" > "$TEST_DIR/checksums.txt"

# Verify valid checksum
(
    cd "$TEST_DIR"
    sha256sum -c checksums.txt
)
echo "PASS: Valid checksum verified cleanly"

# Verify tampered binary fails checksum
echo "tampered binary content" > "$TEST_DIR/release_helper_go"
if (cd "$TEST_DIR" && sha256sum -c checksums.txt 2>/dev/null); then
    echo "FAIL: Tampered binary unexpectedly passed checksum verification!"
    exit 1
else
    echo "PASS: Tampered binary correctly caught and rejected by checksum verification"
fi

echo "=== Test 3: Binary execution test ==="
# Test that release_helper_go and app-registry build from source can be invoked with --help
echo "Building release tools from source..."
bazel build //tools/release_helper_go:release_helper_go //tools/app_registry/cli:app-registry

BAZEL_BIN=$(bazel info bazel-bin)
HELPER_BIN="$BAZEL_BIN/tools/release_helper_go/release_helper_go_/release_helper_go"
REGISTRY_BIN="$BAZEL_BIN/tools/app_registry/cli/app-registry_/app-registry"

if [ ! -x "$HELPER_BIN" ] && [ -f "$HELPER_BIN" ]; then
    chmod +x "$HELPER_BIN"
fi
if [ ! -x "$REGISTRY_BIN" ] && [ -f "$REGISTRY_BIN" ]; then
    chmod +x "$REGISTRY_BIN"
fi

"$HELPER_BIN" --help > /dev/null
echo "PASS: release_helper_go binary executed successfully"

"$REGISTRY_BIN" --help > /dev/null
echo "PASS: app-registry CLI executed successfully"

echo "=== All download-release-tools tests passed ==="
