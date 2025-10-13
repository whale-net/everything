#!/bin/bash
# Test container images for multiple apps
# Usage: ./test_container_images.sh namespace:app1 namespace:app2 ...
# Example: ./test_container_images.sh friendly_computing_machine:fcm-bot manman:experience-api

# Don't exit on error - we want to test all images
set +e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Default platform
PLATFORM="${PLATFORM:-//tools:linux_x86_64}"

if [ $# -eq 0 ]; then
    echo -e "${RED}Error: No apps specified${NC}"
    echo
    echo "Usage: $0 namespace:app1 [namespace:app2 ...]"
    echo
    echo "Examples:"
    echo "  $0 friendly_computing_machine:fcm-bot"
    echo "  $0 manman:experience-api manman:worker"
    echo "  $0 friendly_computing_machine:fcm-bot friendly_computing_machine:fcm-worker"
    echo
    echo "Environment variables:"
    echo "  PLATFORM=//tools:linux_arm64  # Override platform (default: linux_x86_64)"
    exit 1
fi

cd "$REPO_ROOT"

echo -e "${BLUE}Testing Container Images${NC}"
echo "=========================="
echo "Platform: $PLATFORM"
echo

total=0
passed=0
failed=0

for app_spec in "$@"; do
    ((total++))
    
    # Parse namespace:app
    if [[ ! "$app_spec" =~ ^[^:]+:[^:]+$ ]]; then
        echo -e "${RED}✗ Invalid format: $app_spec (expected namespace:app)${NC}"
        ((failed++))
        echo
        continue
    fi
    
    namespace=$(echo "$app_spec" | cut -d: -f1)
    app=$(echo "$app_spec" | cut -d: -f2)
    
    echo -e "${YELLOW}Testing: $namespace/$app${NC}"
    
    # Build and load image
    bazel_target="//$namespace:${app}_image_load"
    echo "  Building: $bazel_target"
    
    build_output=$(bazel run "$bazel_target" --platforms="$PLATFORM" 2>&1)
    build_exit=$?
    
    if [ $build_exit -ne 0 ]; then
        echo -e "${RED}  ✗ Build failed${NC}"
        echo "$build_output" | grep -E "ERROR|error" | head -3 | sed 's/^/    /'
        ((failed++))
        echo
        continue
    fi
    
    # Extract image name from build output
    image_name=$(echo "$build_output" | grep "Loaded image:" | sed 's/.*Loaded image: //')
    
    if [ -z "$image_name" ]; then
        echo -e "${RED}  ✗ Could not determine image name${NC}"
        ((failed++))
        echo
        continue
    fi
    
    echo "  Image: $image_name"
    
    # Test help menu
    help_output=$(docker run --rm "$image_name" --help 2>&1 | head -15)
    
    if echo "$help_output" | grep -q "Usage:"; then
        echo -e "${GREEN}  ✓ Help menu works${NC}"
        ((passed++))
    else
        echo -e "${RED}  ✗ Help menu failed${NC}"
        echo "$help_output" | head -5 | sed 's/^/    /'
        ((failed++))
    fi
    
    echo
done

echo -e "${BLUE}Summary${NC}"
echo "======="
echo "Total:  $total"
echo -e "${GREEN}Passed: $passed${NC}"
if [ $failed -gt 0 ]; then
    echo -e "${RED}Failed: $failed${NC}"
else
    echo "Failed: 0"
fi

if [ $failed -gt 0 ]; then
    exit 1
else
    exit 0
fi
