# Patch Removal Plan: containerd-errdefs-pkg-errhttp

## Overview

This document describes the temporary patch applied to `github.com/containerd/errdefs` and the plan for removing it once upstream issues are resolved.

## Why This Patch Exists

**Problem**: Docker SDK v28.5.2 imports `github.com/containerd/errdefs/pkg/errhttp`, but this package does not exist in any released version of containerd/errdefs (including the latest v1.0.0).

**Impact Without Patch**: Bazel builds fail with:
```
ERROR: no such package '@@gazelle++go_deps+com_github_containerd_errdefs//pkg/errhttp':
BUILD file not found in directory 'pkg/errhttp' of external repository
```

**Patch Solution**: Created a stub `pkg/errhttp` package that provides minimal functionality to satisfy Docker SDK's import requirements.

## Patch Files

- `tools/bazel/patches/containerd-errdefs-pkg-errhttp.patch` - Adds stub pkg/errhttp package
- `MODULE.bazel` lines 148-161 - Applies patch via `go_deps.module_override`

## Patch Contents

The patch adds two files to containerd/errdefs:

1. **pkg/errhttp/BUILD.bazel**: Bazel build configuration for the stub package
2. **pkg/errhttp/errhttp.go**: Minimal stub with `ToNative(statusCode int) error` function

The stub re-exports the parent package's error handling to satisfy Docker SDK imports without breaking functionality.

## When to Remove This Patch

Monitor these upstream sources and remove the patch when ANY of the following occurs:

### Option 1: Docker SDK Fix (Recommended)
- **Repository**: https://github.com/docker/docker
- **Watch For**: New Docker SDK release that either:
  - Removes the `pkg/errhttp` import from `client/errors.go`
  - Updates to a containerd/errdefs version that includes pkg/errhttp
  - Uses a different error handling approach

**How to Check**:
```bash
# View Docker SDK's containerd/errdefs import
go mod graph | grep containerd/errdefs

# Or check Docker SDK source directly
curl -s https://raw.githubusercontent.com/docker/docker/master/client/errors.go | grep errhttp
```

### Option 2: containerd/errdefs Update
- **Repository**: https://github.com/containerd/errdefs
- **Watch For**: New release that adds pkg/errhttp package back

**How to Check**:
```bash
# List containerd/errdefs releases
curl -s https://api.github.com/repos/containerd/errdefs/releases | jq '.[].tag_name'

# Check if pkg/errhttp exists in a version
git clone https://github.com/containerd/errdefs.git
cd errdefs
git checkout <version>
ls -la pkg/errhttp/  # Should exist if package was added
```

## Removal Steps

Once upstream is fixed, follow these steps:

### 1. Update go.mod

If Docker SDK released a fixed version:
```bash
cd /home/alex/whale_net/everything
go get github.com/docker/docker@<fixed-version>
go mod tidy
```

Or if containerd/errdefs released a version with pkg/errhttp:
```bash
go get github.com/containerd/errdefs@<new-version>
go mod tidy
```

### 2. Remove Patch References from MODULE.bazel

Edit `MODULE.bazel` and remove lines 148-161:
```python
# DELETE THIS ENTIRE BLOCK:
# TEMPORARY PATCH: Fix Docker SDK v28.5.2 dependency issue
# ...
go_deps.module_override(
    patch_strip = 1,
    patches = [
        "//tools/bazel/patches:containerd-errdefs-pkg-errhttp.patch",
    ],
    path = "github.com/containerd/errdefs",
)
```

### 3. Delete Patch Files

```bash
rm tools/bazel/patches/containerd-errdefs-pkg-errhttp.patch
```

Update `tools/bazel/patches/BUILD.bazel` to remove the export:
```python
# Remove this line:
"containerd-errdefs-pkg-errhttp.patch",
```

### 4. Verify Build Still Works

```bash
# Clean Bazel cache to ensure fresh build
bazel clean --expunge

# Rebuild Docker library
bazel build //libs/go/docker:docker

# Rebuild wrapper tests
bazel build //manman/wrapper:wrapper_integration_test

# If Docker is available, run integration tests
bazel test //manman/wrapper:wrapper_integration_test --test_tag_filters=integration
```

### 5. Clean Up Documentation

Delete this file:
```bash
rm tools/bazel/patches/PATCH_REMOVAL_PLAN.md
```

Remove references from other docs:
- `manman/wrapper/testdata/BAZEL_LIMITATION.md` - Update to reflect issue is resolved
- `docs/GO_DEPENDENCIES.md` - Remove from Known Issues section

## Verification Checklist

Before removing the patch, verify:
- [ ] Upstream fix is confirmed in released version (not just main branch)
- [ ] `go.mod` is updated to use fixed version
- [ ] `bazel build //libs/go/docker:docker` succeeds without patch
- [ ] `bazel build //manman/wrapper:wrapper_integration_test` succeeds without patch
- [ ] No other code depends on the patched behavior
- [ ] Integration tests pass (if Docker is available)

## Current Status

- **Patch Applied**: 2026-01-29
- **Docker SDK Version**: v28.5.2+incompatible
- **containerd/errdefs Version**: v1.0.0
- **Status**: Active - patch required for builds to succeed
- **Next Review Date**: Check monthly or when updating Docker SDK

## Related Issues

- Docker SDK: https://github.com/docker/docker/blob/master/client/errors.go
- containerd/errdefs releases: https://github.com/containerd/errdefs/releases
- Original investigation: `manman/wrapper/testdata/BAZEL_LIMITATION.md`

## Notes

- This patch only affects Bazel builds; `go test` works without it
- The patch is minimal and low-risk - it just re-exports existing error handling
- The stub implementation is sufficient for Docker SDK's use case
- No runtime behavior changes - purely a build-time fix
