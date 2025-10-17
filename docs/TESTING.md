# Testing Guide

This guide covers testing utilities and best practices for the monorepo.

## Running Tests

The repository uses Bazel's built-in testing capabilities. All tests can be run with:

```bash
# Run all tests
bazel test //...

# Run tests with verbose output
bazel test //... --test_output=all

# Run specific app tests
bazel test //demo/hello_python:test_main
bazel test //demo/hello_go:main_test
bazel test //demo/hello_fastapi:test_main

# Run tests for a specific directory
bazel test //demo/...
```

## Test Configuration

- Test results are cached by default (configured in `.bazelrc`)
- Tests use the `small` size classification for faster execution
- Python tests use pytest framework
- Go tests use standard Go testing package

**No additional test utilities are currently provided** - each app manages its own testing using standard language tooling.

## Common Testing Issues

### Module import errors
Ensure `//libs/python` is included in deps for Python tests

### Cache issues
Use `bazel clean` if you encounter stale test results

### Slow tests
Bazel caches test results - only changed tests will re-run
