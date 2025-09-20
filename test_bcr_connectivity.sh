#!/bin/bash
# BCR Connectivity Test Script
# Use this script to verify that firewall changes have resolved BCR access issues

echo "🔍 Testing BCR Connectivity..."
echo "================================"

# Test primary BCR domain
echo -n "Testing bcr.bazel.build: "
if curl -s --fail --max-time 10 https://bcr.bazel.build/bazel_registry.json > /dev/null 2>&1; then
    echo "✅ ACCESSIBLE"
    BCR_STATUS="OK"
else
    echo "❌ BLOCKED"
    BCR_STATUS="FAILED"
fi

# Test specific module access
echo -n "Testing module access: "
if curl -s --fail --max-time 10 https://bcr.bazel.build/modules/bazel_skylib/1.8.1/MODULE.bazel > /dev/null 2>&1; then
    echo "✅ ACCESSIBLE"
    MODULE_STATUS="OK"
else
    echo "❌ BLOCKED"
    MODULE_STATUS="FAILED"
fi

# Test Docker registry
echo -n "Testing Docker registry: "
if curl -s --fail --max-time 10 https://registry-1.docker.io/v2/ > /dev/null 2>&1; then
    echo "✅ ACCESSIBLE"
    DOCKER_STATUS="OK"
else
    echo "❌ BLOCKED"
    DOCKER_STATUS="FAILED"
fi

# Test GitHub (should work)
echo -n "Testing GitHub access: "
if curl -s --fail --max-time 10 https://github.com > /dev/null 2>&1; then
    echo "✅ ACCESSIBLE"
    GITHUB_STATUS="OK"
else
    echo "❌ BLOCKED"
    GITHUB_STATUS="FAILED"
fi

echo ""
echo "================================"
echo "📊 SUMMARY"
echo "================================"
echo "BCR Access:       $BCR_STATUS"
echo "Module Access:    $MODULE_STATUS"
echo "Docker Registry:  $DOCKER_STATUS"
echo "GitHub Access:    $GITHUB_STATUS"

if [ "$BCR_STATUS" = "OK" ] && [ "$MODULE_STATUS" = "OK" ]; then
    echo ""
    echo "✅ BCR connectivity is working!"
    echo "You can now proceed with Bazel builds:"
    echo "  bazel build //..."
    echo "  bazel test //..."
    exit 0
else
    echo ""
    echo "❌ BCR connectivity issues detected!"
    echo "Firewall still needs to allow access to:"
    if [ "$BCR_STATUS" = "FAILED" ]; then
        echo "  - bcr.bazel.build"
    fi
    if [ "$DOCKER_STATUS" = "FAILED" ]; then
        echo "  - registry-1.docker.io"
    fi
    echo ""
    echo "See BCR_FIREWALL_ANALYSIS.md for detailed instructions."
    exit 1
fi