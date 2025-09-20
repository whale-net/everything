#!/bin/bash
# Test script to validate release tool caching optimizations
# This script measures build times before and after optimization

set -e

echo "=== Release Tool Caching Optimization Test ==="
echo

# Function to measure build time
measure_build_time() {
    local config="$1"
    local description="$2"
    
    echo "Testing: $description"
    
    # Clean to ensure fresh build
    bazel clean > /dev/null 2>&1
    
    # Measure build time
    start_time=$(date +%s)
    if [ -n "$config" ]; then
        bazel build --config="$config" //tools:release > /dev/null 2>&1
    else
        bazel build //tools:release > /dev/null 2>&1
    fi
    end_time=$(date +%s)
    
    build_time=$((end_time - start_time))
    echo "  Build time: ${build_time}s"
    
    return $build_time
}

echo "1. Testing standard build configuration..."
measure_build_time "" "Standard build"
standard_time=$?

echo
echo "2. Testing optimized tools configuration..."
measure_build_time "tools" "Optimized tools build"
optimized_time=$?

echo
echo "3. Testing cached rebuild (should be fast)..."
start_time=$(date +%s)
bazel build --config=tools //tools:release > /dev/null 2>&1
end_time=$(date +%s)
cached_time=$((end_time - start_time))
echo "  Cached rebuild time: ${cached_time}s"

echo
echo "=== Results Summary ==="
echo "Standard build time:    ${standard_time}s"
echo "Optimized build time:   ${optimized_time}s"
echo "Cached rebuild time:    ${cached_time}s"

# Calculate improvement
if [ $standard_time -gt 0 ] && [ $optimized_time -gt 0 ]; then
    improvement=$(( (standard_time - optimized_time) * 100 / standard_time ))
    echo "Optimization improvement: ${improvement}%"
fi

echo
if [ $cached_time -lt 5 ]; then
    echo "✅ Caching is working effectively (cached rebuild < 5s)"
else
    echo "⚠️  Caching may need improvement (cached rebuild >= 5s)"
fi

if [ $optimized_time -lt $standard_time ]; then
    echo "✅ Tool optimization is working (optimized < standard)"
else
    echo "⚠️  Tool optimization may need tuning"
fi

echo
echo "Run this script in CI or local environments with network access to BCR."