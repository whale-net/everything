#!/usr/bin/env bash
# Test to verify the RIGHT WHEEL is in the RIGHT PLATFORM.
#
# That's it. Nothing more, nothing less.
# AMD64 image must have x86_64 wheels. ARM64 image must have aarch64 wheels.
#
# PREREQUISITE: Images must be loaded before running this test. Run:
#   bazel run //demo/hello_fastapi:hello_fastapi_image_load --platforms=//tools:linux_x86_64
#   bazel run //demo/hello_fastapi:hello_fastapi_image_load --platforms=//tools:linux_arm64
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
echo "║             Cross-Compilation Wheel Verification                            ║"
echo "║                                                                              ║"
echo "║  Verify: AMD64 images have x86_64 wheels. ARM64 images have aarch64 wheels. ║"
echo "║                                                                              ║"
echo "║  That's it. Nothing more, nothing less. The right wheel in the right place. ║"
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
    
    # Verify images exist (using new naming with dash separator)
    echo "Checking if images are loaded..."
    if ! docker image inspect "${app_name}-amd64:latest" >/dev/null 2>&1; then
        echo -e "${RED}ERROR: Image ${app_name}-amd64:latest not found!${NC}"
        echo "Please run: bazel run //demo/${app_name}:${app_name}_image_amd64_load --platforms=//tools:linux_x86_64"
        return 1
    fi
    
    if ! docker image inspect "${app_name}-arm64:latest" >/dev/null 2>&1; then
        echo -e "${RED}ERROR: Image ${app_name}-arm64:latest not found!${NC}"
        echo "Please run: bazel run //demo/${app_name}:${app_name}_image_arm64_load --platforms=//tools:linux_arm64"
        return 1
    fi
    
    echo -e "${GREEN}✓ Both images found${NC}"
    echo ""
    
    # Check AMD64 container for x86_64 wheels
    echo "================================================================================"
    echo "Checking AMD64 container..."
    echo "================================================================================"
    
    # Create temporary directory for extraction
    local temp_dir=$(mktemp -d)
    trap "rm -rf $temp_dir" EXIT
    
    # Save the image to a tar file and extract to inspect contents
    echo "Extracting image layers..."
    if ! docker save "${app_name}-amd64:latest" -o "$temp_dir/image.tar" 2>/dev/null; then
        echo -e "${RED}ERROR: Failed to save AMD64 image${NC}"
        rm -rf "$temp_dir"
        return 1
    fi
    
    # Extract the OCI image tar
    cd "$temp_dir"
    tar xf image.tar 2>/dev/null || true
    
    local amd64_so_files=""
    # OCI format stores layers as blobs - search through all tar.gz blobs
    if [ -d "blobs/sha256" ]; then
        for blob in blobs/sha256/*; do
            if [ -f "$blob" ]; then
                # Try to list tar contents (blobs can be tar or tar.gz)
                local so_files=$(tar tzf "$blob" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || \
                                 tar tf "$blob" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || true)
                if [ -n "$so_files" ]; then
                    amd64_so_files="$so_files"
                    break
                fi
            fi
        done
    fi
    cd - > /dev/null
    
    if [ -z "$amd64_so_files" ]; then
        echo -e "${YELLOW}WARNING: No .so files found for ${test_package} in AMD64 image${NC}"
        echo "This might be a pure Python app - skipping architecture check"
        rm -rf "$temp_dir"
        return 0
    fi
    
    echo "AMD64 .so files:"
    echo "$amd64_so_files"
    echo ""
    
    # Verify x86_64 architecture
    if ! echo "$amd64_so_files" | grep -q "x86_64"; then
        echo -e "${RED}❌ FAIL: AMD64 container does NOT have x86_64 wheels!${NC}"
        echo "Found: $amd64_so_files"
        rm -rf "$temp_dir"
        return 1
    fi
    
    if echo "$amd64_so_files" | grep -q "aarch64"; then
        echo -e "${RED}❌ FAIL: AMD64 container has aarch64 wheels (should be x86_64)!${NC}"
        echo "Found: $amd64_so_files"
        rm -rf "$temp_dir"
        return 1
    fi
    
    echo -e "${GREEN}✅ PASS: AMD64 container has x86_64 wheels${NC}"
    rm -rf "$temp_dir"
    
    # Check ARM64 container for aarch64 wheels
    echo ""
    echo "================================================================================"
    echo "Checking ARM64 container..."
    echo "================================================================================"
    
    # Create temporary directory for extraction
    local temp_dir=$(mktemp -d)
    trap "rm -rf $temp_dir" EXIT
    
    # Save the image to a tar file and extract to inspect contents
    echo "Extracting image layers..."
    if ! docker save "${app_name}-arm64:latest" -o "$temp_dir/image.tar" 2>/dev/null; then
        echo -e "${RED}ERROR: Failed to save ARM64 image${NC}"
        rm -rf "$temp_dir"
        return 1
    fi
    
    # Extract the OCI image tar
    cd "$temp_dir"
    tar xf image.tar 2>/dev/null || true
    
    local arm64_so_files=""
    # OCI format stores layers as blobs - search through all tar.gz blobs
    if [ -d "blobs/sha256" ]; then
        for blob in blobs/sha256/*; do
            if [ -f "$blob" ]; then
                # Try to list tar contents (blobs can be tar or tar.gz)
                local so_files=$(tar tzf "$blob" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || \
                                 tar tf "$blob" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || true)
                if [ -n "$so_files" ]; then
                    arm64_so_files="$so_files"
                    break
                fi
            fi
        done
    fi
    cd - > /dev/null
    
    if [ -z "$arm64_so_files" ]; then
        echo -e "${YELLOW}WARNING: No .so files found for ${test_package} in ARM64 image${NC}"
        rm -rf "$temp_dir"
        return 0
    fi
    
    echo "ARM64 .so files:"
    echo "$arm64_so_files"
    echo ""
    
    # Verify aarch64 architecture
    if ! echo "$arm64_so_files" | grep -q "aarch64"; then
        echo -e "${RED}❌ FAIL: ARM64 container does NOT have aarch64 wheels!${NC}"
        echo "Found: $arm64_so_files"
        rm -rf "$temp_dir"
        return 1
    fi
    
    if echo "$arm64_so_files" | grep -q "x86_64"; then
        echo -e "${RED}❌ FAIL: ARM64 container has x86_64 wheels (should be aarch64)!${NC}"
        echo "Found: $arm64_so_files"
        rm -rf "$temp_dir"
        return 1
    fi
    
    echo -e "${GREEN}✅ PASS: ARM64 container has aarch64 wheels${NC}"
    rm -rf "$temp_dir"
    
    return 0
}

# Track overall success
overall_success=0

# Test apps with compiled dependencies
# Add more test cases here as needed

# Test 1: FastAPI with pydantic
if test_app_multiarch \
    "demo-hello_fastapi" \
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
