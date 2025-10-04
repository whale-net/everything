# Cross-Compilation Documentation - DEPRECATED

**⚠️ This document describes the OLD complex cross-compilation system that has been replaced.**

**For the current simplified system, see:**
- **[SIMPLIFIED_MULTIPLATFORM.md](../SIMPLIFIED_MULTIPLATFORM.md)** - Complete guide to the new system
- **[MIGRATION_COMPLETE.md](../MIGRATION_COMPLETE.md)** - Summary of changes and migration

---

## What Changed

The complex platform transition system described in this document has been **completely replaced** with a simpler, more idiomatic approach:

### Old System (Described in this doc)
- Platform transitions with custom Starlark rules
- Multiple binary variants (`_base_amd64`, `_linux_amd64`, etc.)
- Complex wrapper rules and symlinks
- Platform-specific parameters (`binary_amd64`, `binary_arm64`)

### New System (See SIMPLIFIED_MULTIPLATFORM.md)
- Single binary target per app
- Standard Bazel `--platforms` flag for cross-compilation
- No custom transitions or wrapper rules
- Simple, idiomatic Bazel patterns

## How to Use the New System

```bash
# Build for specific platform
bazel build //app:my_app --platforms=//tools:linux_x86_64
bazel build //app:my_app --platforms=//tools:linux_arm64

# Build and load container images
bazel run //app:my_app_image_amd64_load --platforms=//tools:linux_x86_64
bazel run //app:my_app_image_arm64_load --platforms=//tools:linux_arm64
```

The release system handles platform flags automatically, so for production workflows you just use:

```bash
bazel run //tools:release -- build my_app
```

---

## Historical Context (OLD SYSTEM)

The rest of this document describes the old implementation for historical reference only.
**Do not use these patterns - they have been removed from the codebase.**

