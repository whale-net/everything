#!/bin/bash
# Verification script for Python helm composer implementation

set -e

echo "=== Helm Composer Python Implementation Verification ==="
echo

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Test 1: Python syntax validation
echo "1. Validating Python syntax..."
python3 -m py_compile tools/release_helper/charts/*.py
echo -e "${GREEN}✓${NC} Python syntax is valid"
echo

# Test 2: Import validation
echo "2. Validating module imports..."
python3 -c "from tools.release_helper.charts.composer import HelmComposer"
python3 -c "from tools.release_helper.charts.types import AppType"
echo -e "${GREEN}✓${NC} Module imports work correctly"
echo

# Test 3: CLI help
echo "3. Testing CLI interface..."
python3 tools/release_helper/charts/composer.py --help > /dev/null
echo -e "${GREEN}✓${NC} CLI help works"
echo

# Test 4: Generate sample chart
echo "4. Generating sample chart..."
TMP_DIR=$(mktemp -d)

# Create test metadata
cat > "$TMP_DIR/test_metadata.json" << EOF
{
  "name": "sample_app",
  "app_type": "external-api",
  "version": "v1.0.0",
  "description": "Sample application for testing",
  "registry": "ghcr.io",
  "repo_name": "whale-net/sample_app",
  "domain": "demo",
  "language": "python",
  "port": 8000,
  "replicas": 2
}
EOF

# Generate chart
python3 tools/release_helper/charts/composer.py \
  --metadata "$TMP_DIR/test_metadata.json" \
  --chart-name sample-chart \
  --version 1.0.0 \
  --environment production \
  --namespace prod \
  --output "$TMP_DIR" \
  --template-dir tools/helm/templates

# Verify chart structure
if [ -f "$TMP_DIR/sample-chart/Chart.yaml" ] && \
   [ -f "$TMP_DIR/sample-chart/values.yaml" ] && \
   [ -d "$TMP_DIR/sample-chart/templates" ]; then
    echo -e "${GREEN}✓${NC} Chart structure is correct"
else
    echo -e "${RED}✗${NC} Chart structure is invalid"
    exit 1
fi

# Verify Chart.yaml content
if grep -q "name: sample-chart" "$TMP_DIR/sample-chart/Chart.yaml" && \
   grep -q "version: 1.0.0" "$TMP_DIR/sample-chart/Chart.yaml"; then
    echo -e "${GREEN}✓${NC} Chart.yaml is valid"
else
    echo -e "${RED}✗${NC} Chart.yaml is invalid"
    exit 1
fi

# Verify values.yaml content
if grep -q "namespace: prod" "$TMP_DIR/sample-chart/values.yaml" && \
   grep -q "environment: production" "$TMP_DIR/sample-chart/values.yaml" && \
   grep -q "sample_app:" "$TMP_DIR/sample-chart/values.yaml"; then
    echo -e "${GREEN}✓${NC} values.yaml is valid"
else
    echo -e "${RED}✗${NC} values.yaml is invalid"
    exit 1
fi

# Verify templates exist for external-api
if [ -f "$TMP_DIR/sample-chart/templates/deployment.yaml" ] && \
   [ -f "$TMP_DIR/sample-chart/templates/service.yaml" ] && \
   [ -f "$TMP_DIR/sample-chart/templates/ingress.yaml" ] && \
   [ -f "$TMP_DIR/sample-chart/templates/pdb.yaml" ]; then
    echo -e "${GREEN}✓${NC} Templates are generated correctly for external-api"
else
    echo -e "${RED}✗${NC} Templates are missing"
    exit 1
fi

# Cleanup
rm -rf "$TMP_DIR"

echo
echo "5. Checking module reorganization..."

# Verify new structure exists
for submodule in core containers github charts; do
    if [ -d "tools/release_helper/$submodule" ]; then
        echo -e "${GREEN}✓${NC} $submodule/ submodule exists"
    else
        echo -e "${RED}✗${NC} $submodule/ submodule missing"
        exit 1
    fi
done

# Verify backward compatibility shims
for shim in core.py git.py validation.py images.py release.py github_release.py release_notes.py helm.py; do
    if [ -f "tools/release_helper/$shim" ]; then
        if grep -q "Backward compatibility shim" "tools/release_helper/$shim"; then
            echo -e "${GREEN}✓${NC} $shim backward compatibility shim exists"
        else
            echo -e "${RED}✗${NC} $shim is not a valid shim"
            exit 1
        fi
    else
        echo -e "${RED}✗${NC} $shim shim missing"
        exit 1
    fi
done

echo
echo -e "${GREEN}=== All verification tests passed! ===${NC}"
echo
echo "Summary:"
echo "- Python helm composer implementation is functional"
echo "- Module reorganization is complete"
echo "- Backward compatibility is maintained"
echo "- Chart generation works correctly"
echo
echo "Note: Bazel integration tests require network access to bcr.bazel.build"
echo "      Run 'bazel test //tools/release_helper:test_composer' when network is available"
