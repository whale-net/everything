# Bazel Integration Limitation for Wrapper Tests

## Issue

The wrapper integration tests cannot currently run in Bazel due to a complex dependency issue with the Docker SDK.

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

## Current Workaround

**Tests work perfectly with Go's native test runner:**

```bash
# Build test image
cd manman/wrapper/testdata
docker build -t manman-test-game-server:latest .

# Run tests with go test
cd ..
go test -tags=integration -v .
```

## Potential Solutions (Future Work)

1. **Wait for Docker SDK fix**: The Docker SDK may release a version compatible with released containerd/errdefs versions

2. **Patch approach**: Create a Bazel patch file to manually add missing dependencies to Docker SDK's BUILD file

3. **Custom BUILD override**: Use `go_repository` with a custom build_file to completely replace Docker SDK's BUILD configuration

4. **Fork approach**: Temporarily fork containerd/errdefs with a stub pkg/errhttp package (not recommended)

5. **Downgrade Docker SDK**: Try an older Docker SDK version that doesn't import pkg/errhttp (may lose needed features)

## Impact

- Tests must be run manually with `go test` on machines with Docker
- CI cannot run integration tests until this is resolved or Docker is added to CI
- Manual testing required before merging wrapper changes

## References

- Docker SDK: github.com/docker/docker v28.5.2+incompatible
- Containerd errdefs: github.com/containerd/errdefs v1.0.0
- Related docs: ../../docs/GO_DEPENDENCIES.md (Known Issues section)
