#!/bin/bash
set -euo pipefail

echo "=== Running all tests ==="
bazel test //...

echo "=== Running Python app ==="
bazel run //hello_python:hello_python

echo "=== Running Go app ==="
bazel run //hello_go:hello_go

echo "=== All checks passed! ==="
