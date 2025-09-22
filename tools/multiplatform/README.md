# Multiplatform Container Testing

This directory contains tests and documentation for the multiplatform container functionality.

## Files

### Tests (Run Manually)
- `test_multiplatform_image.py` - Comprehensive test suite for multiplatform functionality
- `test_multiplatform_integration.py` - Integration tests that run within Bazel sandbox
- `test_multiplatform_configuration.py` - Configuration validation tests  
- `smoke_test_multiplatform.sh` - Shell-based smoke tests for quick validation

**Note**: These tests are designed to run manually outside of Bazel since they execute external Bazel commands. They are exported as files but do not have Bazel test targets.

### Documentation
- `MULTIPLATFORM_IMPLEMENTATION_SUMMARY.md` - Complete implementation summary and status

## Running Tests

### Bazel Tests
```bash
# Run all multiplatform tests
bazel test //tools/multiplatform/...

# Run specific tests
bazel test //tools/multiplatform:test_multiplatform_configuration
bazel test //tools/multiplatform:test_multiplatform_integration
bazel test //tools/multiplatform:test_multiplatform_image
```

### Manual Tests
```bash
# Run comprehensive test suite
python3 tools/multiplatform/test_multiplatform_image.py

# Run smoke tests
./tools/multiplatform/smoke_test_multiplatform.sh
```

## Test Categories

### Configuration Tests (`test_multiplatform_configuration.py`)
- Fast, sandbox-safe tests that validate configuration files
- Checks BUILD.bazel files, platform definitions, etc.
- Safe to run in CI/CD pipelines

### Integration Tests (`test_multiplatform_integration.py`)
- Tests that run bazel commands within test environment
- Validates actual build functionality
- May require network access for container images

### Comprehensive Tests (`test_multiplatform_image.py`)
- Full end-to-end validation of multiplatform functionality
- Builds containers, tests manifests, validates outputs
- Takes longer to run but provides complete coverage

### Smoke Tests (`smoke_test_multiplatform.sh`)
- Quick shell-based validation
- Good for manual testing and debugging
- Tests the most critical functionality paths

## When to Run

- **Configuration tests**: Always safe to run, include in CI
- **Integration tests**: Run when validating build changes
- **Comprehensive tests**: Run before major releases or when multiplatform code changes
- **Smoke tests**: Run during development for quick validation