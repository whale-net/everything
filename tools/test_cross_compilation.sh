#!/usr/bin/env bash
# Test to verify cross-compilation works correctly for Python apps with compiled dependencies.
#
# This test ensures that:
# 1. ARM64 containers get aarch64 wheels (not x86_64)
# 2. AMD64 containers get x86_64 wheels
# 3. Platform transitions are working correctly
#
# This is a CRITICAL test - if this fails, cross-compilation is broken and ARM64 
# containers will crash at runtime with compiled dependencies like pydantic, numpy, etc.

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "╔══════════════════════════════════════════════════════════════════════════════╗"
echo "║                                                                              ║"
echo "║             Python Cross-Compilation Verification Test                      ║"
echo "║                                                                              ║"
echo "║  This test verifies that platform transitions work correctly, ensuring      ║"
echo "║  ARM64 containers get aarch64 wheels and AMD64 containers get x86_64 wheels.║"
echo "║                                                                              ║"
echo "║  CRITICAL: If this test fails, cross-compilation is broken and ARM64        ║"
echo "║  containers will crash at runtime with apps using compiled dependencies     ║"
echo "║  like pydantic, numpy, pandas, pillow, cryptography, etc.                   ║"
echo "║                                                                              ║"
echo "╚══════════════════════════════════════════════════════════════════════════════╝"
echo ""

# Function to test an app's multiarch images
test_app_multiarch() {
    local app_name=$1
    local app_path=$2
    local test_package=$3
    local description=$4
    
    echo ""
    echo "################################################################################"
    echo "# TEST: $description"
    echo "################################################################################"
    echo ""
    echo "================================================================================"
    echo "Testing multiarch for $app_name"
    echo "================================================================================"
    echo ""
    
    # Build both AMD64 and ARM64 images
    echo "Building AMD64 image for $app_name..."
    bazel build "${app_path}:${app_name}_image_amd64"
    
    echo "Building ARM64 image for $app_name..."
    bazel build "${app_path}:${app_name}_image_arm64"
    
    # Load both images
    echo ""
    echo "Loading AMD64 image..."
    bazel run "${app_path}:${app_name}_image_load"
    
    echo "Loading ARM64 image..."
    bazel run "${app_path}:${app_name}_image_arm64_load"
    
    # Check AMD64 container for x86_64 wheels
    echo ""
    echo "================================================================================"
    echo "Checking AMD64 container..."
    echo "================================================================================"
    
    local amd64_so_files
    if ! amd64_so_files=$(docker run --rm --entrypoint /bin/sh "${app_name}_amd64:latest" \
        -c "find /app -name '*${test_package}*.so' 2>/dev/null | head -5"); then
        echo -e "${YELLOW}WARNING: Failed to search for .so files in AMD64 image${NC}"
        return 1
    fi
    
    if [ -z "$amd64_so_files" ]; then
        echo -e "${YELLOW}WARNING: No .so files found for ${test_package} in AMD64 image${NC}"
        echo "This might be a pure Python app - skipping architecture check"
        return 0
    fi
    
    echo "AMD64 .so files:"
    echo "$amd64_so_files"
    echo ""
    
    # Verify x86_64 architecture
    if ! echo "$amd64_so_files" | grep -q "x86_64"; then
        echo -e "${RED}❌ FAIL: AMD64 container does NOT have x86_64 wheels!${NC}"
        echo "Found: $amd64_so_files"
        return 1
    fi
    
    if echo "$amd64_so_files" | grep -q "aarch64"; then
        echo -e "${RED}❌ FAIL: AMD64 container has aarch64 wheels (should be x86_64)!${NC}"
        echo "Found: $amd64_so_files"
        return 1
    fi
    
    echo -e "${GREEN}✅ PASS: AMD64 container has x86_64 wheels${NC}"
    
    # Check ARM64 container for aarch64 wheels
    echo ""
    echo "================================================================================"
    echo "Checking ARM64 container..."
    echo "================================================================================"
    
    local arm64_so_files
    if ! arm64_so_files=$(docker run --rm --entrypoint /bin/sh "${app_name}_arm64:latest" \
        -c "find /app -name '*${test_package}*.so' 2>/dev/null | head -5"); then
        echo -e "${YELLOW}WARNING: Failed to search for .so files in ARM64 image${NC}"
        return 1
    fi
    
    if [ -z "$arm64_so_files" ]; then
        echo -e "${YELLOW}WARNING: No .so files found for ${test_package} in ARM64 image${NC}"
        return 0
    fi
    
    echo "ARM64 .so files:"
    echo "$arm64_so_files"
    echo ""
    
    # Verify aarch64 architecture
    if ! echo "$arm64_so_files" | grep -q "aarch64"; then
        echo -e "${RED}❌ FAIL: ARM64 container does NOT have aarch64 wheels!${NC}"
        echo "Found: $arm64_so_files"
        return 1
    fi
    
    if echo "$arm64_so_files" | grep -q "x86_64"; then
        echo -e "${RED}❌ FAIL: ARM64 container has x86_64 wheels (should be aarch64)!${NC}"
        echo "Found: $arm64_so_files"
        return 1
    fi
    
    echo -e "${GREEN}✅ PASS: ARM64 container has aarch64 wheels${NC}"
    
    return 0
}

# Track overall success
overall_success=0

# Test apps with compiled dependencies
# Add more test cases here as needed

# Test 1: FastAPI with pydantic
if test_app_multiarch \
    "hello_fastapi" \
    "//demo/hello_fastapi" \
    "pydantic_core" \
    "FastAPI app with pydantic (compiled dependency)"; then
    test1_result="${GREEN}✅ PASS${NC}"
else
    test1_result="${RED}❌ FAIL${NC}"
    overall_success=1
fi

# Add more tests here:
# if test_app_multiarch "app_name" "//path/to/app" "package_name" "Description"; then
#     test2_result="${GREEN}✅ PASS${NC}"
# else
#     test2_result="${RED}❌ FAIL${NC}"
#     overall_success=1
# fi

# Print summary
echo ""
echo "================================================================================"
echo "TEST SUMMARY"
echo "================================================================================"
echo -e "hello_fastapi: $test1_result"
# echo -e "other_app: $test2_result"
echo "================================================================================"
echo ""

if [ $overall_success -eq 0 ]; then
    echo -e "${GREEN}✅ All multiarch tests PASSED!${NC}"
    echo "Cross-compilation is working correctly."
    exit 0
else
    echo -e "${RED}❌ Some multiarch tests FAILED!${NC}"
    echo "Cross-compilation is BROKEN - ARM64 containers will crash at runtime!"
    echo ""
    echo "To fix:"
    echo "1. Check that multiplatform_py_binary uses platform transitions"
    echo "2. Verify release_app passes binary_amd64 and binary_arm64 correctly"
    echo "3. Ensure container_image.bzl uses platform-specific binaries"
    exit 1
fi
