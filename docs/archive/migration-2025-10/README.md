# Multiplatform Simplification Migration - October 2025

This directory contains historical documentation from the October 2025 migration that simplified the multiplatform container build system.

## Context

The repository originally used a complex custom architecture with:
- Custom wrapper macros (`multiplatform_py_binary`, `multiplatform_go_binary`)
- Platform transitions (Starlark rules to change `--platforms` flag)
- AppInfo provider system for metadata
- Multiple binary variants per app (`_base_amd64`, `_base_arm64`, `_linux_amd64`, `_linux_arm64`)

This was replaced with idiomatic Bazel patterns:
- Standard `py_binary` and `go_binary` rules
- Direct use of `--platforms` flag at build time
- Metadata passed directly to `release_app` macro
- Single binary per app, built for different platforms via flags

## Migration Documents

### Planning
- **[MULTIPLAT_MIGRATION_PLAN.md](MULTIPLAT_MIGRATION_PLAN.md)** - Original migration plan outlining the simplified approach

### Implementation Progress
- **[SIMPLIFICATION_COMPLETE.md](SIMPLIFICATION_COMPLETE.md)** - First phase: removing platform transitions and wrapper complexity
- **[COMPLETE_SIMPLIFICATION.md](COMPLETE_SIMPLIFICATION.md)** - Documentation of complete wrapper removal
- **[FINAL_SIMPLIFICATION.md](FINAL_SIMPLIFICATION.md)** - Final migration summary and validation
- **[MIGRATION_COMPLETE.md](MIGRATION_COMPLETE.md)** - Early migration checkpoint

### Test Fixes and Verification
- **[TEST_FIXES_COMPLETE.md](TEST_FIXES_COMPLETE.md)** - Documentation of test fixes after removing wrappers

### New Architecture Documentation
- **[SIMPLIFIED_MULTIPLATFORM.md](SIMPLIFIED_MULTIPLATFORM.md)** - Explanation of the new simplified approach (useful reference)

## Outcome

The migration was successful:
- ✅ Removed ~250 lines of custom wrapper code
- ✅ Deleted 3 custom .bzl files
- ✅ All 24 tests passing
- ✅ Standard Bazel idioms throughout
- ✅ Simpler mental model for developers
- ✅ Fixed `oci_image_index` usage to follow rules_oci documentation

## Current Documentation

For current build instructions, see:
- [/README.md](../../../README.md) - Main project documentation
- [/.github/copilot-instructions.md](../../../.github/copilot-instructions.md) - Build patterns and examples
- [/tools/README.md](../../../tools/README.md) - Release system documentation

## Related

- **Active PR**: [#135 - multiplatform redo](https://github.com/whale-net/everything/pull/135)
- **Branch**: `20251004-04`
- **Date**: October 4, 2025
