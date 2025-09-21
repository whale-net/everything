#!/bin/bash
set -euo pipefail

# Multiplatform Image Smoke Tests
# Quick validation that core functionality works

echo "🚀 Running multiplatform image smoke tests..."

WORKSPACE_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$WORKSPACE_ROOT"

echo ""
echo "🧪 Test 1: Build platform-specific images"
echo "Building AMD64 images..."
bazel build //demo/hello_python:hello_python_image_amd64 --platforms=//tools:linux_x86_64
bazel build //demo/hello_go:hello_go_image_amd64 --platforms=//tools:linux_x86_64

echo "Building ARM64 images..."
bazel build //demo/hello_python:hello_python_image_arm64 --platforms=//tools:linux_arm64
bazel build //demo/hello_go:hello_go_image_arm64 --platforms=//tools:linux_arm64

echo "✅ Platform-specific builds successful"

echo ""
echo "🧪 Test 2: Build multi-platform manifests with experimental approach"
bazel build //demo/hello_python:hello_python_image
bazel build //demo/hello_go:hello_go_image

echo "✅ Multi-platform manifest builds successful"

echo ""
echo "🧪 Test 3: Load and test AMD64 containers"
bazel run //demo/hello_python:hello_python_image_amd64_load --platforms=//tools:linux_x86_64
bazel run //demo/hello_go:hello_go_image_amd64_load --platforms=//tools:linux_x86_64

echo "Testing container outputs..."
PYTHON_OUTPUT=$(docker run --rm ghcr.io/whale-net/demo-hello_python:latest-amd64 2>/dev/null)
GO_OUTPUT=$(docker run --rm ghcr.io/whale-net/demo-hello_go:latest-amd64 2>/dev/null)

if [[ "$PYTHON_OUTPUT" == *"Hello, world from uv and Bazel"* ]]; then
    echo "✅ Python container output correct"
else
    echo "❌ Python container output incorrect: $PYTHON_OUTPUT"
    exit 1
fi

if [[ "$GO_OUTPUT" == *"Hello, world from Bazel from Go"* ]]; then
    echo "✅ Go container output correct"
else
    echo "❌ Go container output incorrect: $GO_OUTPUT"
    exit 1
fi

echo ""
echo "🧪 Test 4: Verify manifest structure"
PYTHON_MANIFEST="bazel-bin/demo/hello_python/hello_python_image/index.json"
GO_MANIFEST="bazel-bin/demo/hello_go/hello_go_image/index.json"

if [[ -f "$PYTHON_MANIFEST" ]]; then
    PYTHON_MANIFESTS=$(jq '.manifests | length' "$PYTHON_MANIFEST")
    echo "✅ Python manifest exists with $PYTHON_MANIFESTS entry(ies)"
else
    echo "❌ Python manifest not found"
    exit 1
fi

if [[ -f "$GO_MANIFEST" ]]; then
    GO_MANIFESTS=$(jq '.manifests | length' "$GO_MANIFEST")
    echo "✅ Go manifest exists with $GO_MANIFESTS entry(ies)"
else
    echo "❌ Go manifest not found"
    exit 1
fi

echo ""
echo "🧪 Test 5: Check cross-platform dependencies"
echo "Checking AMD64 Python dependencies..."
AMD64_WHEEL=$(docker run --rm --entrypoint=/bin/sh ghcr.io/whale-net/demo-hello_fastapi:latest-amd64 -c "find /app -name '*pydantic_core*' | grep x86_64 | head -1" 2>/dev/null || echo "")
if [[ -n "$AMD64_WHEEL" ]]; then
    echo "✅ AMD64 Python wheels found"
else
    echo "❌ AMD64 Python wheels not found"
    exit 1
fi

echo ""
echo "🎉 All smoke tests passed!"
echo ""
echo "📋 Summary:"
echo "  ✅ Platform-specific builds work"
echo "  ✅ Experimental multi-platform approach works"
echo "  ✅ Container loading and execution works"
echo "  ✅ Manifest files are generated"
echo "  ✅ Cross-platform dependencies are correct"
echo ""
echo "✨ Multiplatform image functionality is working correctly!"