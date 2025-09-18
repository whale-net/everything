#!/bin/bash

# Unit Tests for Individual Helm Templates
# Tests each template file separately for correctness

set -e

CHART_DIR="$(dirname "$0")"
OUTPUT_DIR="$CHART_DIR/test-output"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Running Unit Tests for Helm Templates${NC}"

mkdir -p "$OUTPUT_DIR"

# Function to test individual templates
test_template() {
    local template_name="$1"
    local description="$2"
    local should_exist="$3"  # true/false

    echo -e "\n${BLUE}Testing: $template_name${NC}"
    echo "Description: $description"

    # Test with default values
    local output=$(helm template manman-test "$CHART_DIR" -s "templates/$template_name" 2>/dev/null || echo "FAILED")

    if [ "$should_exist" = "true" ]; then
        if [ "$output" != "FAILED" ] && [ -n "$output" ]; then
            echo -e "${GREEN}✓ Template renders successfully${NC}"

            # Validate YAML syntax
            if echo "$output" | kubectl --dry-run=client apply -f - > /dev/null 2>&1; then
                echo -e "${GREEN}✓ Generated YAML is valid${NC}"
            else
                echo -e "${RED}✗ Generated YAML is invalid${NC}"
                return 1
            fi

            # Save output for inspection
            echo "$output" > "$OUTPUT_DIR/$template_name.yaml"

        else
            echo -e "${RED}✗ Template failed to render${NC}"
            return 1
        fi
    else
        if [ "$output" = "FAILED" ] || [ -z "$output" ]; then
            echo -e "${GREEN}✓ Template correctly disabled/empty${NC}"
        else
            echo -e "${RED}✗ Template should be disabled but rendered content${NC}"
            return 1
        fi
    fi
}

# Function to test template with specific values
test_template_with_values() {
    local template_name="$1"
    local values_file="$2"
    local description="$3"
    local expected_count="$4"  # Expected number of resources

    echo -e "\n${BLUE}Testing: $template_name with $values_file${NC}"
    echo "Description: $description"

    local output=$(helm template manman-test "$CHART_DIR" -s "templates/$template_name" -f "$values_file" 2>/dev/null || echo "FAILED")

    if [ "$output" != "FAILED" ]; then
        echo -e "${GREEN}✓ Template renders with custom values${NC}"

        # Count resources if expected count is provided
        if [ -n "$expected_count" ]; then
            local actual_count=$(echo "$output" | grep -c "^---" || echo "0")
            # Adjust for YAML document separators
            actual_count=$((actual_count == 0 ? ([ -n "$output" ] && echo "1" || echo "0") : actual_count))

            if [ "$actual_count" -eq "$expected_count" ]; then
                echo -e "${GREEN}✓ Expected number of resources generated ($expected_count)${NC}"
            else
                echo -e "${RED}✗ Resource count mismatch: expected $expected_count, got $actual_count${NC}"
            fi
        fi

        # Save output
        echo "$output" > "$OUTPUT_DIR/$template_name-$(basename "$values_file" .yaml).yaml"
    else
        echo -e "${RED}✗ Template failed with custom values${NC}"
        return 1
    fi
}

# Test all templates with default values
echo -e "${YELLOW}\n=== Testing Templates with Default Values ===${NC}"

test_template "experience-api-deployment.yaml" "Experience API deployment" true
test_template "experience-api-service.yaml" "Experience API service" true
test_template "experience-api-pdb.yaml" "Experience API PDB" true

test_template "worker-dal-api-deployment.yaml" "Worker DAL API deployment" true
test_template "worker-dal-api-service.yaml" "Worker DAL API service" true
test_template "worker-dal-api-pdb.yaml" "Worker DAL API PDB" true

test_template "status-api-deployment.yaml" "Status API deployment" true
test_template "status-api-service.yaml" "Status API service" true
test_template "status-api-pdb.yaml" "Status API PDB" true

test_template "status-processor-deployment.yaml" "Status processor deployment" true
test_template "status-processor-pdb.yaml" "Status processor PDB" true

test_template "migration-job.yaml" "Database migration job" true
test_template "ingress.yaml" "Ingress resource (should be empty by default)" false

# Test templates with different configurations
echo -e "${YELLOW}\n=== Testing Templates with Custom Values ===${NC}"

test_template_with_values "ingress.yaml" "$CHART_DIR/test-values/dev-ingress.yaml" "Ingress with dev configuration" 1
test_template_with_values "experience-api-pdb.yaml" "$CHART_DIR/test-values/production.yaml" "PDB with production values" 1

# Test conditional rendering
echo -e "${YELLOW}\n=== Testing Conditional Template Rendering ===${NC}"

# Create a test values file with some services disabled
cat > "$OUTPUT_DIR/disabled-services.yaml" << EOF
image:
  name: "test"
  tag: "test"
namespace: manman
apis:
  experience:
    enabled: false
  workerDal:
    enabled: true
    name: manman-worker-dal
    replicas: 1
    port: 8000
    command: "start-worker-dal-api"
  status:
    enabled: false
processors:
  status:
    enabled: false
env:
  rabbitmq:
    port: 5672
    host: "test"
    user: "test"
    password: "test"
    enable_ssl: false
    ssl_hostname: "test"
  app_env: test
  db:
    url: "test"
EOF

# Test that disabled services don't render
test_template_with_values "experience-api-deployment.yaml" "$OUTPUT_DIR/disabled-services.yaml" "Disabled experience API" 0
test_template_with_values "experience-api-pdb.yaml" "$OUTPUT_DIR/disabled-services.yaml" "Disabled experience API PDB" 0
test_template_with_values "status-api-deployment.yaml" "$OUTPUT_DIR/disabled-services.yaml" "Disabled status API" 0
test_template_with_values "status-processor-deployment.yaml" "$OUTPUT_DIR/disabled-services.yaml" "Disabled status processor" 0

# Test that enabled service still renders
test_template_with_values "worker-dal-api-deployment.yaml" "$OUTPUT_DIR/disabled-services.yaml" "Enabled worker DAL API" 1

# Validate specific template contents
echo -e "${YELLOW}\n=== Validating Template Contents ===${NC}"

validate_template_content() {
    local template_file="$1"
    local pattern="$2"
    local description="$3"

    if grep -q "$pattern" "$template_file"; then
        echo -e "${GREEN}✓ $description${NC}"
    else
        echo -e "${RED}✗ $description${NC}"
        return 1
    fi
}

# Validate PDB templates have correct structure
for service in experience worker-dal status status-processor; do
    pdb_file="$OUTPUT_DIR/${service}-api-pdb.yaml"
    if [ "$service" = "status-processor" ]; then
        pdb_file="$OUTPUT_DIR/status-processor-pdb.yaml"
    fi

    if [ -f "$pdb_file" ]; then
        echo -e "\nValidating $service PDB content..."
        validate_template_content "$pdb_file" "kind: PodDisruptionBudget" "$service PDB has correct kind"
        validate_template_content "$pdb_file" "minAvailable: 1" "$service PDB has minAvailable: 1"
        validate_template_content "$pdb_file" "matchLabels:" "$service PDB has selector"
    fi
done

echo -e "\n${GREEN}Unit tests completed!${NC}"
echo -e "Template outputs saved in: $OUTPUT_DIR"
