#!/bin/bash
# Integration test for job disablement via values file
# This test validates that jobs (like migration) can be disabled via the enabled flag

set -e

echo "=== Job Disablement Integration Test ==="
echo ""

# Create a temporary directory for testing
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

echo "Test directory: $TEMP_DIR"
echo ""

# Build the hello_job chart (which is a job type)
echo "Building hello_job chart..."
cd /home/runner/work/everything/everything
bazel build //demo/hello_job:hello_job_chart 2>&1 | tail -5
CHART_TARBALL="bazel-bin/demo/hello_job/helm-demo-hello_job.tar.gz"

if [ ! -f "$CHART_TARBALL" ]; then
    echo "✗ Chart tarball not found: $CHART_TARBALL"
    exit 1
fi
echo "✓ Chart built successfully"
echo ""

# Extract the chart
echo "Extracting chart..."
tar -xzf "$CHART_TARBALL" -C "$TEMP_DIR"
CHART_DIR="$TEMP_DIR/demo-hello_job"
echo "✓ Chart extracted to $CHART_DIR"
echo ""

# Test 1: Verify values.yaml has enabled field
echo "Test 1: Verifying enabled field exists in values.yaml..."
if ! grep -q "enabled: true" "$CHART_DIR/values.yaml"; then
    echo "✗ values.yaml missing 'enabled: true' field"
    cat "$CHART_DIR/values.yaml"
    exit 1
fi
echo "✓ enabled field found in values.yaml"
echo ""

# Test 2: Render chart with default values (enabled=true)
echo "Test 2: Rendering chart with default values (enabled=true)..."
RENDERED=$(helm template test-release "$CHART_DIR" 2>&1)
if ! echo "$RENDERED" | grep -q "kind: Job"; then
    echo "✗ Job resource not found when enabled=true"
    echo "$RENDERED"
    exit 1
fi
echo "✓ Job resource rendered when enabled=true"
echo ""

# Test 3: Create custom values with enabled=false
echo "Test 3: Creating custom values with enabled=false..."
cat > "$TEMP_DIR/custom-values.yaml" <<EOF
apps:
  hello_job:
    enabled: false
EOF
echo "✓ Custom values created"
echo ""

# Test 4: Render chart with enabled=false
echo "Test 4: Rendering chart with enabled=false..."
RENDERED_DISABLED=$(helm template test-release "$CHART_DIR" --values "$TEMP_DIR/custom-values.yaml" 2>&1)
if echo "$RENDERED_DISABLED" | grep -q "kind: Job"; then
    echo "✗ Job resource still rendered when enabled=false"
    echo "$RENDERED_DISABLED"
    exit 1
fi
echo "✓ Job resource NOT rendered when enabled=false"
echo ""

# Test 5: Verify only the job is disabled (check for presence of comments/metadata but not Job kind)
echo "Test 5: Verifying chart renders but without Job..."
if [ -z "$RENDERED_DISABLED" ]; then
    echo "✗ Chart rendered nothing (expected at least Chart metadata)"
    exit 1
fi
echo "✓ Chart renders without Job when disabled"
echo ""

echo "=== All Job Disablement Tests Passed! ==="
