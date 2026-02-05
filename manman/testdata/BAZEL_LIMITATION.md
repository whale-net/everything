# Bazel Integration Status for Wrapper Tests

## Status: RESOLVED ✓

The wrapper integration tests now build successfully in Bazel. The Docker SDK dependency issue has been resolved with a temporary patch.

## Root Cause

The Docker SDK v28.5.2 imports `github.com/containerd/errdefs/pkg/errhttp`, but this package does not exist in any released version of containerd/errdefs (including the latest v1.0.0).

When Bazel's gazelle generates BUILD files for the Docker SDK, it creates a dependency on this non-existent package, causing build failures:

```
ERROR: no such package '@@gazelle++go_deps+com_github_containerd_errdefs//pkg/errhttp':
BUILD file not found in directory 'pkg/errhttp' of external repository
```

## Investigation Summary

1. **Gazelle directives attempted**: Added `gazelle_override` in MODULE.bazel to manually specify Docker dependencies - partially worked but couldn't resolve the missing pkg/errhttp package

2. **Version check**: Confirmed containerd/errdefs v1.0.0 is the latest, and pkg/errhttp doesn't exist in the repository structure

3. **Import verification**: The import exists in Docker SDK source code (`client/errors.go`) without build tags, suggesting it's always required

4. **Upgrade attempted**: Tried upgrading containerd/errdefs to latest - no change (v1.0.0 is already latest)

## Solution Applied

A temporary patch has been applied to fix the Docker SDK dependency issue. See `tools/bazel/patches/PATCH_REMOVAL_PLAN.md` for details and removal instructions.

**Building with Bazel:**
```bash
# Build wrapper tests
bazel build //manman/wrapper:wrapper_integration_test

# Run tests (requires Docker)
bazel test //manman/wrapper:wrapper_integration_test --test_tag_filters=integration
```

**Alternative: Tests also work with Go's native test runner:**
```bash
# Build test image
cd manman/wrapper/testdata
docker build -t manman-test-game-server:latest .

# Run tests with go test
cd ..
go test -tags=integration -v .
```

## Solution Details

**Chosen Approach**: Patch containerd/errdefs with stub pkg/errhttp package

This approach was selected because it:
- Adds minimal code (stub package that re-exports parent functionality)
- Doesn't modify Docker SDK (upstream dependency)
- Works with current Docker SDK v28.5.2
- Has clear removal path when upstream is fixed
- Low maintenance burden

**Files Modified**:
- `tools/bazel/patches/containerd-errdefs-pkg-errhttp.patch` - Stub package implementation
- `MODULE.bazel` - Applies patch via go_deps.module_override
- `libs/go/docker/container.go` - Updated to Docker SDK v28 API
- `tools/bazel/patches/PATCH_REMOVAL_PLAN.md` - Removal instructions

**Other Approaches Considered**:
1. Wait for Docker SDK fix - Would block development indefinitely
2. Custom BUILD override - More complex, harder to maintain
3. Fork containerd/errdefs - Creates maintenance burden
4. Downgrade Docker SDK - May lose needed features

## Impact (Resolved)

- ✓ Tests now build successfully with Bazel
- ✓ Integration tests can run with Bazel (requires Docker on build machine)
- ⚠️ CI still cannot run integration tests (no Docker in CI environment)
- ℹ️ Manual testing with `go test` or `bazel test` required before merging wrapper changes
- ℹ️ Patch will need removal when upstream is fixed (see PATCH_REMOVAL_PLAN.md)

## References

- Docker SDK: github.com/docker/docker v28.5.2+incompatible
- Containerd errdefs: github.com/containerd/errdefs v1.0.0
- Related docs: ../../docs/GO_DEPENDENCIES.md (Known Issues section)
