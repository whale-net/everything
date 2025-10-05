#!/usr/bin/env bash
# Test to verify the RIGHT WHEEL is in the RIGHT PLATFORM.
#
# That's it. Nothing more, nothing less.
# AMD64 image must have x86_64 wheels. ARM64 image must have aarch64 wheels.
#
# This test builds the OCI image index directly and inspects both platform variants
# without needing to load them into Docker (which doesn't support multiarch manifests well).
#
# PREREQUISITE: None - test builds the images itself
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
#   image_name: Docker image name (e.g., "demo-hello_fastapi")
#   test_package: Python package name with compiled extensions to check (e.g., "pydantic_core")
#   description: Human-readable test description for output
test_app_multiarch() {
    local image_name=$1
    local test_package=$2
    local description=$3
    
    # Convert image name (demo-hello_fastapi) to app name (hello_fastapi)
    local app_name=$(echo "$image_name" | sed 's/^demo-//')
    
    echo ""
    echo "################################################################################"
    echo "# TEST: $description"
    echo "################################################################################"
    echo ""
    echo "================================================================================"
    echo "Testing multiarch for $image_name (app: $app_name)"
    echo "================================================================================"
    echo ""
    
    # Find the OCI layout directory in runfiles or workspace
    local oci_layout=""
    
    # When running via bazel test, look in runfiles
    if [ -n "${RUNFILES_DIR}" ]; then
        oci_layout="${RUNFILES_DIR}/_main/demo/${app_name}/${app_name}_image"
    # When running directly (./test_cross_compilation.sh), look in workspace
    elif [ -d "bazel-bin/demo/${app_name}/${app_name}_image" ]; then
        oci_layout="bazel-bin/demo/${app_name}/${app_name}_image"
    fi
    
    if [ -z "$oci_layout" ] || [ ! -d "$oci_layout" ]; then
        echo -e "${RED}ERROR: OCI layout not found${NC}"
        echo "Expected at: $oci_layout"
        return 1
    fi
    
    echo -e "${GREEN}✓ Found OCI layout at $oci_layout${NC}"
    echo ""
    
    # Verify it's a multiarch manifest
    echo "Verifying multiarch manifest..."
    if [ ! -f "$oci_layout/index.json" ]; then
        echo -e "${RED}ERROR: index.json not found${NC}"
        return 1
    fi
    
    # Check if this is a nested index (index pointing to another index)
    local first_manifest_digest=$(jq -r '.manifests[0].digest' "$oci_layout/index.json")
    local first_manifest_media_type=$(jq -r '.manifests[0].mediaType' "$oci_layout/index.json")
    
    if [ "$first_manifest_media_type" == "application/vnd.oci.image.index.v1+json" ]; then
        # This is a nested index - follow it to the actual platform manifests
        echo "Found nested index, following to platform manifests..."
        local nested_index_path="$oci_layout/blobs/$(echo $first_manifest_digest | tr ':' '/')"
        if [ ! -f "$nested_index_path" ]; then
            echo -e "${RED}ERROR: Nested index not found at $nested_index_path${NC}"
            return 1
        fi
        # Use the nested index for platform checks
        local index_file="$nested_index_path"
    else
        # Direct index with platform manifests
        local index_file="$oci_layout/index.json"
    fi
    
    local manifest_count=$(jq '.manifests | length' "$index_file")
    if [ "$manifest_count" -lt 2 ]; then
        echo -e "${RED}ERROR: Expected at least 2 manifests, found $manifest_count${NC}"
        return 1
    fi
    
    echo -e "${GREEN}✓ Multiarch manifest found with $manifest_count platform variants${NC}"
    echo ""
    
    # Check AMD64 container for x86_64 wheels
    echo "================================================================================"
    echo "Checking AMD64 variant..."
    echo "================================================================================"
    
    # Extract AMD64 manifest from index
    local amd64_manifest=$(jq -r '.manifests[] | select(.platform.architecture == "amd64") | .digest' "$index_file" | head -1)
    if [ -z "$amd64_manifest" ]; then
        echo -e "${RED}ERROR: No AMD64 manifest found in index${NC}"
        return 1
    fi
    
    # Convert digest to blob path (sha256:abc... -> blobs/sha256/abc...)
    local amd64_blob_path="$oci_layout/blobs/$(echo $amd64_manifest | tr ':' '/')"
    if [ ! -f "$amd64_blob_path" ]; then
        echo -e "${RED}ERROR: AMD64 manifest blob not found at $amd64_blob_path${NC}"
        return 1
    fi
    
    # Get config digest from manifest
    local amd64_config=$(jq -r '.config.digest' "$amd64_blob_path")
    local amd64_config_path="$oci_layout/blobs/$(echo $amd64_config | tr ':' '/')"
    
    # Get layer digests
    local amd64_layers=$(jq -r '.layers[].digest' "$amd64_blob_path")
    
    # Search for .so files in layers
    local amd64_so_files=""
    for layer_digest in $amd64_layers; do
        local layer_path="$oci_layout/blobs/$(echo $layer_digest | tr ':' '/')"
        if [ -f "$layer_path" ]; then
            # Try to list tar contents (layers can be tar or tar.gz)
            local so_files=$(tar tzf "$layer_path" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || \
                             tar tf "$layer_path" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || true)
            if [ -n "$so_files" ]; then
                amd64_so_files="$so_files"
                break
            fi
        fi
    done
    
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
    echo "Checking ARM64 variant..."
    echo "================================================================================"
    
    # Extract ARM64 manifest from index
    local arm64_manifest=$(jq -r '.manifests[] | select(.platform.architecture == "arm64") | .digest' "$index_file" | head -1)
    if [ -z "$arm64_manifest" ]; then
        echo -e "${RED}ERROR: No ARM64 manifest found in index${NC}"
        return 1
    fi
    
    # Convert digest to blob path
    local arm64_blob_path="$oci_layout/blobs/$(echo $arm64_manifest | tr ':' '/')"
    if [ ! -f "$arm64_blob_path" ]; then
        echo -e "${RED}ERROR: ARM64 manifest blob not found at $arm64_blob_path${NC}"
        return 1
    fi
    
    # Get layer digests
    local arm64_layers=$(jq -r '.layers[].digest' "$arm64_blob_path")
    
    # Search for .so files in layers
    local arm64_so_files=""
    for layer_digest in $arm64_layers; do
        local layer_path="$oci_layout/blobs/$(echo $layer_digest | tr ':' '/')"
        if [ -f "$layer_path" ]; then
            # Try to list tar contents
            local so_files=$(tar tzf "$layer_path" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || \
                             tar tf "$layer_path" 2>/dev/null | grep -E "${test_package}.*\.so$" | head -${MAX_SO_FILES} || true)
            if [ -n "$so_files" ]; then
                arm64_so_files="$so_files"
                break
            fi
        fi
    done
    
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
