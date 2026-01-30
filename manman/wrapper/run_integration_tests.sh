#!/bin/bash
# Helper script to run wrapper integration tests locally
# Requires Docker to be running

set -e

echo "=== ManMan Wrapper Integration Tests ==="
echo ""

# Check if Docker is available
if ! command -v docker &> /dev/null; then
    echo "ERROR: Docker is not installed or not in PATH"
    exit 1
fi

# Check if Docker daemon is running
if ! docker info &> /dev/null; then
    echo "ERROR: Docker daemon is not running"
    exit 1
fi

echo "✓ Docker is available"
echo ""

# Build test image
echo "Building test container image..."
cd "$(dirname "$0")/testdata"
docker build -t manman-test-game-server:latest .
echo "✓ Test image built successfully"
echo ""

# Run tests
cd ..
echo "Running integration tests..."
echo ""

# Option 1: Using Go directly (easier for local development)
if command -v go &> /dev/null; then
    echo "Using Go test runner..."
    go test -tags=integration -v -timeout=5m .
else
    # Option 2: Using Bazel (if Go not available)
    echo "Using Bazel test runner..."
    cd ../../..
    bazel test //manman/wrapper:wrapper_integration_test --test_tag_filters=integration --test_output=all
fi

echo ""
echo "=== All tests passed! ==="
