#!/bin/bash
# Integration test for helm_chart rule
# This script validates that generated Helm charts are valid and can be linted

set -e

echo "=== Helm Chart Integration Test ==="
echo ""

# In the Bazel sandbox, the chart is already built and available in runfiles
# Find the tarball in the runfiles directory
# Note: Chart tarballs use the internal name (helm-demo-hello-fastapi)
# but the extracted directory uses the published name (demo-hello-fastapi)
RUNFILES_DIR="${RUNFILES_DIR:-$0.runfiles}"
if [ -d "${RUNFILES_DIR}/_main/demo" ]; then
    CHART_TARBALL="${RUNFILES_DIR}/_main/demo/helm-demo-hello-fastapi.tar.gz"
    CHART_NAME="demo-hello-fastapi"
elif [ -d "demo" ]; then
    # Running outside Bazel
    CHART_TARBALL="bazel-bin/demo/helm-demo-hello-fastapi.tar.gz"
    CHART_NAME="demo-hello-fastapi"
    if [ ! -f "$CHART_TARBALL" ]; then
        echo "Building fastapi_chart..."
        bazel build //demo:fastapi_chart
        echo "✓ Build succeeded"
        echo ""
    fi
else
    echo "✗ Cannot find chart tarball"
    exit 1
fi

if [ ! -f "$CHART_TARBALL" ]; then
    echo "✗ Chart tarball not found: $CHART_TARBALL"
    exit 1
fi

echo "Using chart tarball: $CHART_TARBALL"
echo ""

# Extract the chart tarball to a temp directory
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

echo "Extracting chart to $TEMP_DIR..."
tar -xzf "$CHART_TARBALL" -C $TEMP_DIR
CHART_DIR="$TEMP_DIR/$CHART_NAME"
echo "✓ Chart extracted"
echo ""

# Run helm lint
echo "Running helm lint..."
helm lint "$CHART_DIR"
echo "✓ Helm lint passed"
echo ""

# Validate Chart.yaml structure
echo "Validating Chart.yaml..."
if ! grep -q "apiVersion: v2" "$CHART_DIR/Chart.yaml"; then
    echo "✗ Chart.yaml missing apiVersion v2"
    exit 1
fi
if ! grep -q "name: $CHART_NAME" "$CHART_DIR/Chart.yaml"; then
    echo "✗ Chart.yaml has incorrect name (expected: $CHART_NAME)"
    cat "$CHART_DIR/Chart.yaml" | grep "name:"
    exit 1
fi
echo "✓ Chart.yaml valid"
echo ""

# Validate values.yaml structure
echo "Validating values.yaml..."
if ! grep -q "demo-hello-fastapi:" "$CHART_DIR/values.yaml"; then
    echo "✗ values.yaml missing demo-hello-fastapi app (expected domain-app format)"
    exit 1
fi
echo "✓ values.yaml valid"
echo ""

# Check that templates exist
echo "Validating templates..."
for template in deployment.yaml service.yaml ingress.yaml pdb.yaml; do
    if [ ! -f "$CHART_DIR/templates/$template" ]; then
        echo "✗ Missing template: $template"
        exit 1
    fi
done
echo "✓ All templates present"
echo ""

# Try to render the chart with helm template (dry-run)
echo "Testing chart rendering with helm template..."
helm template test-release "$CHART_DIR" > /dev/null
echo "✓ Chart renders successfully"
echo ""

echo "=== All Integration Tests Passed! ==="
