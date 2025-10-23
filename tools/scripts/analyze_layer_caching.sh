#!/usr/bin/env bash
# Analyze Bazel caching for container image layers
#
# This script helps understand which layers will be rebuilt when
# different types of changes are made to the codebase.

set -euo pipefail

APP=${1:-}

if [ -z "$APP" ]; then
    echo "Usage: $0 <app_target>"
    echo "Example: $0 //demo/hello_fastapi:hello-fastapi_image_base"
    exit 1
fi

echo "Analyzing layer caching for: $APP"
echo "=========================================="
echo

# Extract app name
APP_NAME=$(echo "$APP" | sed 's|.*:||' | sed 's|_image_base||')
APP_DIR=$(echo "$APP" | sed 's|//||' | sed 's|:.*||')

# Function to get action key for a target
get_action_info() {
    local target=$1
    bazel aquery "$target" 2>/dev/null | grep -A 5 "action 'Genrule" || echo "Not found"
}

echo "Layer Dependency Chain:"
echo "-----------------------"
echo

# Analyze each layer
for layer in python deps app; do
    echo "📦 ${layer}_layer target: ${APP}_${layer}_layer"
    
    # Get the dependencies
    deps=$(bazel query "deps(${APP}_${layer}_layer)" 2>/dev/null | head -20)
    
    # Count dependencies
    dep_count=$(echo "$deps" | wc -l)
    echo "   Dependencies: $dep_count transitive targets"
    
    # Show key dependencies
    echo "   Key inputs:"
    echo "$deps" | grep -E "(\.py$|\.bzl$|strip_python|_full_runfiles)" | head -5 | sed 's/^/     - /'
    
    echo
done

echo
echo "Caching Behavior Analysis:"
echo "-------------------------"
echo

echo "1️⃣  Python Interpreter Layer (_python_layer)"
echo "   ✅ Cached when: Only app code or dependencies change"
echo "   ❌ Rebuilt when:"
echo "      - Python version changes (e.g., 3.13.0 -> 3.13.1)"
echo "      - Target platform changes (--platforms flag)"
echo "      - strip_python.sh script changes"
echo

echo "2️⃣  Dependencies Layer (_deps_layer)"
echo "   ✅ Cached when: Only app code changes"
echo "   ❌ Rebuilt when:"
echo "      - uv.lock changes (dependency add/update/remove)"
echo "      - Wheel artifacts change for target platform"
echo "      - strip_python.sh script changes"
echo

echo "3️⃣  App Code Layer (_app_layer)"
echo "   ✅ Cached when: Only unrelated files change"
echo "   ❌ Rebuilt when:"
echo "      - App source files change (*.py in $APP_DIR)"
echo "      - Local library files change (//libs/python)"
echo "      - Binary metadata changes"
echo

echo
echo "💡 Tips for Maximum Cache Efficiency:"
echo "======================================"
echo "• During development: Only app layer rebuilds (fastest!)"
echo "• When updating deps: Python + app layers cached"
echo "• When changing Python version: Only deps + app layers cached"
echo "• Use remote cache to share layers across developers/CI"
echo

echo "Remote Cache Command Examples:"
echo "------------------------------"
echo "# Build with remote cache"
echo "bazel build $APP --remote_cache=grpc://cache.example.com:9092"
echo

echo "# Check cache hit rate"
echo "bazel build $APP --execution_log_json_file=execution.log"
echo "cat execution.log | jq '.[] | select(.remoteCacheHit == true)' | wc -l"
echo
