#!/bin/bash

# Pod Disruption Budget Specific Tests
# Validates PDB functionality and configuration

set -e

CHART_DIR="$(dirname "$0")"
OUTPUT_DIR="$CHART_DIR/test-output"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Testing Pod Disruption Budgets${NC}"

mkdir -p "$OUTPUT_DIR"

# Test PDB with different replica counts
test_pdb_with_replicas() {
    local replicas="$1"
    local test_name="pdb-replicas-$replicas"

    echo -e "\n${YELLOW}Testing PDB behavior with $replicas replicas${NC}"

    # Create temporary values file
    cat > "$OUTPUT_DIR/$test_name-values.yaml" << EOF
image:
  name: "ghcr.io/whale-net/manman"
  tag: "test"
namespace: manman
apis:
  experience:
    enabled: true
    name: manman-experience
    replicas: $replicas
    port: 8000
    command: "start-experience-api"
  workerDal:
    enabled: true
    name: manman-worker-dal
    replicas: $replicas
    port: 8000
    command: "start-worker-dal-api"
  status:
    enabled: true
    name: manman-status
    replicas: $replicas
    port: 8000
    command: "start-status-api"
processors:
  status:
    enabled: true
    name: manman-status-processor
    replicas: $replicas
    command: "start-status-processor"
env:
  rabbitmq:
    port: 5672
    host: "test-rabbitmq"
    user: "test"
    password: "test"
    enable_ssl: false
    ssl_hostname: "test"
  app_env: test
  db:
    url: "postgresql+psycopg2://test:test@test:5432/test"
EOF

    # Render templates
    if helm template manman-test "$CHART_DIR" -f "$OUTPUT_DIR/$test_name-values.yaml" > "$OUTPUT_DIR/$test_name.yaml" 2>/dev/null; then
        echo -e "${GREEN}✓ Template rendering successful for $replicas replicas${NC}"

        # Count PDBs
        local pdb_count=$(grep -c "kind: PodDisruptionBudget" "$OUTPUT_DIR/$test_name.yaml")
        if [ "$pdb_count" -eq 4 ]; then
            echo -e "${GREEN}✓ All 4 PDBs generated${NC}"
        else
            echo -e "${RED}✗ Expected 4 PDBs, found $pdb_count${NC}"
        fi

        # Validate minAvailable is always 1
        local min_available_count=$(grep -c "minAvailable: 1" "$OUTPUT_DIR/$test_name.yaml")
        if [ "$min_available_count" -eq 4 ]; then
            echo -e "${GREEN}✓ All PDBs have minAvailable: 1${NC}"
        else
            echo -e "${RED}✗ Some PDBs don't have minAvailable: 1 (found $min_available_count/4)${NC}"
        fi

        # Check deployment replica counts
        local deployment_replicas=$(grep "replicas:" "$OUTPUT_DIR/$test_name.yaml" | head -1 | awk '{print $2}')
        if [ -n "$deployment_replicas" ] && [ "$deployment_replicas" -eq "$replicas" ]; then
            echo -e "${GREEN}✓ Deployment replicas set correctly to $replicas${NC}"
        else
            echo -e "${RED}✗ Deployment replicas mismatch (expected: $replicas, found: ${deployment_replicas:-'none'})${NC}"
        fi

    else
        echo -e "${RED}✗ Template rendering failed for $replicas replicas${NC}"
        return 1
    fi
}

# Test conditional PDB creation
test_conditional_pdb() {
    echo -e "\n${YELLOW}Testing conditional PDB creation${NC}"

    # Create values with some services disabled
    cat > "$OUTPUT_DIR/conditional-values.yaml" << EOF
image:
  name: "ghcr.io/whale-net/manman"
  tag: "test"
namespace: manman
apis:
  experience:
    enabled: true
    name: manman-experience
    replicas: 1
    port: 8000
    command: "start-experience-api"
  workerDal:
    enabled: false  # Disabled
    name: manman-worker-dal
    replicas: 1
    port: 8000
    command: "start-worker-dal-api"
  status:
    enabled: true
    name: manman-status
    replicas: 1
    port: 8000
    command: "start-status-api"
processors:
  status:
    enabled: false  # Disabled
    name: manman-status-processor
    replicas: 1
    command: "start-status-processor"
env:
  rabbitmq:
    port: 5672
    host: "test-rabbitmq"
    user: "test"
    password: "test"
    enable_ssl: false
    ssl_hostname: "test"
  app_env: test
  db:
    url: "postgresql+psycopg2://test:test@test:5432/test"
EOF

    if helm template manman-test "$CHART_DIR" -f "$OUTPUT_DIR/conditional-values.yaml" > "$OUTPUT_DIR/conditional.yaml" 2>/dev/null; then
        echo -e "${GREEN}✓ Template rendering successful${NC}"

        # Should only have 2 PDBs (experience and status)
        local pdb_count=$(grep -c "kind: PodDisruptionBudget" "$OUTPUT_DIR/conditional.yaml" || echo "0")
        if [ "$pdb_count" -eq 2 ]; then
            echo -e "${GREEN}✓ Only enabled services have PDBs (2 found)${NC}"
        else
            echo -e "${RED}✗ Expected 2 PDBs for enabled services, found $pdb_count${NC}"
        fi

        # Check that disabled services don't have PDBs
        if ! grep -q "manman-worker-dal.*pdb" "$OUTPUT_DIR/conditional.yaml"; then
            echo -e "${GREEN}✓ Disabled worker-dal service has no PDB${NC}"
        else
            echo -e "${RED}✗ Disabled worker-dal service should not have PDB${NC}"
        fi

        if ! grep -q "manman-status-processor.*pdb" "$OUTPUT_DIR/conditional.yaml"; then
            echo -e "${GREEN}✓ Disabled status-processor service has no PDB${NC}"
        else
            echo -e "${RED}✗ Disabled status-processor service should not have PDB${NC}"
        fi

    else
        echo -e "${RED}✗ Template rendering failed${NC}"
        return 1
    fi
}

# Test PDB selector labels
test_pdb_selectors() {
    echo -e "\n${YELLOW}Testing PDB selector labels${NC}"

    helm template manman-test "$CHART_DIR" -f "$CHART_DIR/test-values/all-enabled.yaml" > "$OUTPUT_DIR/selector-test.yaml" 2>/dev/null

    # Extract PDB selectors and deployment labels to ensure they match
    local services=("experience" "worker-dal" "status" "status-processor")

    for service in "${services[@]}"; do
        echo -e "\nValidating $service PDB selector..."

        # Find the PDB selector for this service
        local pdb_selector=$(grep -A 10 "name: manman-.*$service.*-pdb" "$OUTPUT_DIR/selector-test.yaml" | grep -A 2 "matchLabels:" | grep "app:" | awk '{print $2}')

        # Find the corresponding deployment label
        local deployment_label=""
        if [[ "$service" == "status-processor" ]]; then
            deployment_label=$(grep -A 10 "name: manman-status-processor" "$OUTPUT_DIR/selector-test.yaml" | grep -A 5 "matchLabels:" | grep "app:" | awk '{print $2}')
        else
            deployment_label=$(grep -A 10 "name: manman-.*$service" "$OUTPUT_DIR/selector-test.yaml" | grep -A 5 "matchLabels:" | grep "app:" | awk '{print $2}')
        fi

        if [ "$pdb_selector" = "$deployment_label" ] && [ -n "$pdb_selector" ]; then
            echo -e "${GREEN}✓ $service PDB selector matches deployment labels${NC}"
        else
            echo -e "${RED}✗ $service PDB selector mismatch${NC}"
            echo "  PDB selector: $pdb_selector"
            echo "  Deployment label: $deployment_label"
        fi
    done
}

# Run all PDB tests
echo -e "${YELLOW}Starting PDB test suite...${NC}"

# Test with different replica counts
test_pdb_with_replicas 1
test_pdb_with_replicas 2
test_pdb_with_replicas 3

# Test conditional creation
test_conditional_pdb

# Test selector labels
test_pdb_selectors

echo -e "\n${GREEN}PDB tests completed!${NC}"
