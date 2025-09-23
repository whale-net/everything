# Multiplatform Container Testing

This directory contains tests and documentation for the multiplatform container functionality.

## Architecture Decision: Language-Specific Patterns

### Why We Use Different Patterns for Go vs Python

While `oci_image_index` supports two approaches for creating multi-platform images, we deliberately use different patterns for different languages based on their runtime characteristics:

#### Go Applications: Ideal Pattern (With platforms parameter)
```starlark
go_binary(name = "app")
tar(name = "app_layer", srcs = [":app"])
oci_image(name = "image", tars = [":app_layer"])
oci_image_index(
    name = "image_multiarch",
    images = [":image"],
    platforms = [
        "@rules_go//go/toolchain:linux_amd64",
        "@rules_go//go/toolchain:linux_arm64",
    ],
)
```
**Why this works for Go**: Go binaries are statically linked and cross-compile cleanly without platform-specific runtime dependencies.

#### Python Applications: Explicit Platform Pattern
```starlark
# Separate platform-specific binaries
multiplatform_py_binary(name = "app")  # Creates app, app_linux_amd64, app_linux_arm64

# Separate platform-specific images
multiplatform_python_image(
    name = "image",
    binary_amd64 = ":app_linux_amd64",
    binary_arm64 = ":app_linux_arm64",
)
# This creates separate oci_image targets and combines them with oci_image_index
```
**Why Python needs explicit handling**:
1. **Platform-specific dependencies**: Python wheels are often architecture-specific (native extensions)
2. **Runtime environment**: Python requires platform-specific interpreters and libraries
3. **Dependency resolution**: We need separate pip requirements for each platform (`requirements.linux.amd64.lock.txt`, `requirements.linux.arm64.lock.txt`)

### Implementation Decision

**We maintain pattern consistency within each language rather than forcing consistency across languages with different runtime models.** This ensures:
- ✅ Reliable platform-specific dependency resolution for Python
- ✅ Clean, simple patterns for Go applications  
- ✅ Production-tested approaches for both languages
- ✅ Future maintainability as language ecosystems evolve

The `platforms` parameter in `oci_image_index` is marked as "highly EXPERIMENTAL" and rules_oci documentation shows no Python examples using it, further validating our decision to use the explicit approach for Python.

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