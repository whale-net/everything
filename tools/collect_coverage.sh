#!/bin/bash
# Collect and merge test coverage from Bazel tests
# Usage: ./tools/collect_coverage.sh [output_dir]

set -euo pipefail

OUTPUT_DIR="${1:-coverage_output}"
WORKSPACE_ROOT="$(bazel info workspace)"

echo "Collecting coverage data from Bazel tests..."
echo "Workspace: $WORKSPACE_ROOT"
echo "Output directory: $OUTPUT_DIR"

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Run tests with coverage
# For Python tests, Bazel generates coverage.dat files
echo "Running tests with coverage instrumentation..."
bazel coverage //... || {
    echo "Warning: Some tests may have failed, but continuing with coverage collection"
}

# Bazel generates coverage.dat in bazel-out/_coverage/_coverage_report.dat
COVERAGE_FILE="$(bazel info output_path)/_coverage/_coverage_report.dat"

if [ -f "$COVERAGE_FILE" ]; then
    echo "Found coverage report at: $COVERAGE_FILE"
    
    # Copy the lcov file to output directory
    cp "$COVERAGE_FILE" "$OUTPUT_DIR/coverage.lcov"
    echo "Coverage report copied to: $OUTPUT_DIR/coverage.lcov"
    
    # Generate HTML report if genhtml is available
    if command -v genhtml &> /dev/null; then
        echo "Generating HTML coverage report..."
        genhtml "$OUTPUT_DIR/coverage.lcov" -o "$OUTPUT_DIR/html" --ignore-errors source
        echo "HTML report available at: $OUTPUT_DIR/html/index.html"
    fi
else
    echo "Error: Coverage report not found at $COVERAGE_FILE"
    echo "Available files in coverage directory:"
    ls -la "$(bazel info output_path)/_coverage/" || echo "Coverage directory not found"
    exit 1
fi

echo ""
echo "Coverage collection complete!"
echo "Coverage file: $OUTPUT_DIR/coverage.lcov"
echo ""
echo "To upload to Codecov, run:"
echo "  bash <(curl -s https://codecov.io/bash) -f $OUTPUT_DIR/coverage.lcov"
