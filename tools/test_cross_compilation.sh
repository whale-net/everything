#!/usr/bin/env bash
# Test to verify cross-compilation works correctly for Python apps with compiled dependencies.
#
# PREREQUISITE: Images must be loaded before running this test. Run:
#   bazel run //demo/hello_fastapi:hello_fastapi_image_amd64_load
#   bazel run //demo/hello_fastapi:hello_fastapi_image_arm64_load
#
# This test ensures that:
# 1. ARM64 containers get aarch64 wheels (not x86_64)
# 2. AMD64 containers get x86_64 wheels
# 3. Cross-platform wheel selection (rules_pycross) is working correctly
#
# This is a CRITICAL test - if this fails, cross-compilation is broken and ARM64 
# containers will crash at runtime with compiled dependencies like pydantic, numpy, etc.

set -euo pipefail

# Maximum number of .so files to check per container (limits output size)
MAX_SO_FILES=5

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
echo "║  This test verifies that cross-platform builds work correctly, ensuring     ║"
echo "║  ARM64 containers get aarch64 wheels and AMD64 containers get x86_64 wheels.║"
echo "║                                                                              ║"
echo "║  CRITICAL: If this test fails, cross-compilation is broken and ARM64        ║"
echo "║  containers will crash at runtime with apps using compiled dependencies     ║"
echo "║  like pydantic, numpy, pandas, pillow, cryptography, etc.                   ║"
echo "║                                                                              ║"
echo "║  NOTE: Images must be loaded before running this test (see script header)   ║"
echo "║                                                                              ║"
echo "╚══════════════════════════════════════════════════════════════════════════════╝"
echo ""

# Function to test an app's multiarch images
# Args:
#   app_name: Base name of the app (e.g., "hello_fastapi")
#   test_package: Python package name with compiled extensions to check (e.g., "pydantic_core")
#   description: Human-readable test description for output
test_app_multiarch() {
    local app_name=$1
    local test_package=$2
    local description=$3
    
    echo ""
    echo "################################################################################"
    echo "# TEST: $description"
    echo "################################################################################"
    echo ""
    echo "================================================================================"
    echo "Testing multiarch for $app_name"
    echo "================================================================================"
    echo ""
    
    # Verify images exist
    echo "Checking if images are loaded..."
    if ! docker image inspect "${app_name}_amd64:latest" >/dev/null 2>&1; then
        echo -e "${RED}ERROR: Image ${app_name}_amd64:latest not found!${NC}"
        echo "Please run: bazel run //demo/${app_name}:${app_name}_image_amd64_load"
        return 1
    fi
    
    if ! docker image inspect "${app_name}_arm64:latest" >/dev/null 2>&1; then
        echo -e "${RED}ERROR: Image ${app_name}_arm64:latest not found!${NC}"
        echo "Please run: bazel run //demo/${app_name}:${app_name}_image_arm64_load"
        return 1
    fi
    
    echo -e "${GREEN}✓ Both images found${NC}"
    echo ""
    
    # Check AMD64 container for x86_64 wheels
    echo "================================================================================"
    echo "Checking AMD64 container..."
    echo "================================================================================"
    
    local amd64_so_files
    if ! amd64_so_files=$(docker run --rm --entrypoint /bin/sh "${app_name}_amd64:latest" \
        -c "find /app -name '*${test_package}*.so' 2>/dev/null | head -${MAX_SO_FILES}"); then
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
        -c "find /app -name '*${test_package}*.so' 2>/dev/null | head -${MAX_SO_FILES}"); then
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
    "pydantic_core" \
    "FastAPI app with pydantic (compiled dependency)"; then
    test1_result="${GREEN}✅ PASS${NC}"
else
    test1_result="${RED}❌ FAIL${NC}"
    overall_success=1
fi

# Add more tests here:
# if test_app_multiarch "app_name" "package_name" "Description"; then
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
    echo "1. Verify rules_pycross is resolving wheels for both platforms (check uv.lock)"
    echo "2. Ensure --platforms=//tools:linux_x86_64 and //tools:linux_arm64 are used"
    echo "3. Check container_image.bzl uses platform-specific oci_image targets"
    exit 1
fi
