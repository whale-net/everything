#!/bin/bash

# Helm Chart Test Suite for ManMan Host
# This script tests template rendering and validates the generated Kubernetes manifests

set -e

CHART_DIR="$(dirname "$0")"
TEST_VALUES_DIR="$CHART_DIR/test-values"
OUTPUT_DIR="$CHART_DIR/test-output"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Starting ManMan Host Helm Chart Tests${NC}"

# Clean up previous test outputs
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

# Function to run a test
run_test() {
    local test_name="$1"
    local values_file="$2"
    local description="$3"

    echo -e "\n${YELLOW}Running test: $test_name${NC}"
    echo "Description: $description"

    if helm template manman-test "$CHART_DIR" -f "$values_file" > "$OUTPUT_DIR/$test_name.yaml" 2>/dev/null; then
        echo -e "${GREEN}✓ Template rendering successful${NC}"

        # Validate YAML syntax
        if kubectl --dry-run=client apply -f "$OUTPUT_DIR/$test_name.yaml" > /dev/null 2>&1; then
            echo -e "${GREEN}✓ Generated manifests are valid Kubernetes YAML${NC}"
        else
            echo -e "${RED}✗ Generated manifests have invalid YAML syntax${NC}"
            return 1
        fi
    else
        echo -e "${RED}✗ Template rendering failed${NC}"
        helm template manman-test "$CHART_DIR" -f "$values_file" 2>&1 | head -10
        return 1
    fi
}

# Function to validate specific resources exist
validate_resource() {
    local output_file="$1"
    local resource_type="$2"
    local resource_name="$3"
    local test_name="$4"

    if grep -q "kind: $resource_type" "$output_file" && grep -q "name: $resource_name" "$output_file"; then
        echo -e "${GREEN}✓ $resource_type $resource_name found in $test_name${NC}"
    else
        echo -e "${RED}✗ $resource_type $resource_name not found in $test_name${NC}"
        return 1
    fi
}

# Function to validate PDB configuration
validate_pdb() {
    local output_file="$1"
    local pdb_name="$2"
    local test_name="$3"

    if grep -A 5 -B 5 "name: $pdb_name" "$output_file" | grep -q "minAvailable: 1"; then
        echo -e "${GREEN}✓ PDB $pdb_name has correct minAvailable setting${NC}"
    else
        echo -e "${RED}✗ PDB $pdb_name missing or incorrect minAvailable setting${NC}"
        return 1
    fi
}

echo -e "\n${YELLOW}Test 1: Default configuration with all services enabled${NC}"
run_test "default-all-enabled" "$TEST_VALUES_DIR/all-enabled.yaml" "All services enabled with default settings"

echo -e "\n${YELLOW}Test 2: Minimal configuration with only required services${NC}"
run_test "minimal-config" "$TEST_VALUES_DIR/minimal.yaml" "Only essential services enabled"

echo -e "\n${YELLOW}Test 3: Production-like configuration${NC}"
run_test "production-config" "$TEST_VALUES_DIR/production.yaml" "Production settings with higher replicas"

echo -e "\n${YELLOW}Test 4: Development configuration with ingress${NC}"
run_test "dev-with-ingress" "$TEST_VALUES_DIR/dev-ingress.yaml" "Development setup with ingress enabled"

echo -e "\n${YELLOW}Validating generated resources...${NC}"

# Validate deployments exist
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "Deployment" "manman-experience-dev" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "Deployment" "manman-worker-dal-dev" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "Deployment" "manman-status-dev" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "Deployment" "manman-status-processor-dev" "default-all-enabled"

# Validate services exist
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "Service" "manman-experience-dev" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "Service" "manman-worker-dal-dev" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "Service" "manman-status-dev" "default-all-enabled"

# Validate PDBs exist and are configured correctly
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "PodDisruptionBudget" "manman-experience-dev-pdb" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "PodDisruptionBudget" "manman-worker-dal-dev-pdb" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "PodDisruptionBudget" "manman-status-dev-pdb" "default-all-enabled"
validate_resource "$OUTPUT_DIR/default-all-enabled.yaml" "PodDisruptionBudget" "manman-status-processor-dev-pdb" "default-all-enabled"

# Validate PDB configurations
validate_pdb "$OUTPUT_DIR/default-all-enabled.yaml" "manman-experience-dev-pdb" "default-all-enabled"
validate_pdb "$OUTPUT_DIR/default-all-enabled.yaml" "manman-worker-dal-dev-pdb" "default-all-enabled"
validate_pdb "$OUTPUT_DIR/default-all-enabled.yaml" "manman-status-dev-pdb" "default-all-enabled"
validate_pdb "$OUTPUT_DIR/default-all-enabled.yaml" "manman-status-processor-dev-pdb" "default-all-enabled"

echo -e "\n${GREEN}All tests completed successfully!${NC}"
echo -e "Test outputs saved in: $OUTPUT_DIR"
