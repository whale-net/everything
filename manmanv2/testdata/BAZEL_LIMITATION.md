# Bazel Integration Status for Wrapper Tests

## Status: RESOLVED ✓

The wrapper integration tests build successfully in Bazel. `github.com/containerd/errdefs/pkg/errhttp`, which Docker SDK v28.5.2 imports, ships as its own Go module (`github.com/containerd/errdefs/pkg`, currently pinned at v0.3.0 in `go.mod`) rather than as a subpackage of `github.com/containerd/errdefs` (v1.0.0). Once that module is present in `go.mod`, `go_deps` resolves it to its own external repo with the real `pkg/errhttp` package, so no patch is needed.

A temporary stub patch (`tools/bazel/patches/containerd-errdefs-pkg-errhttp.patch`) was previously applied to work around this before the split module was pulled in; it has since been removed as dead weight.

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

## Impact (Resolved)

- ✓ Tests now build successfully with Bazel
- ✓ Integration tests can run with Bazel (requires Docker on build machine)
- ⚠️ CI still cannot run integration tests (no Docker in CI environment)
- ℹ️ Manual testing with `go test` or `bazel test` required before merging wrapper changes

## References

- Docker SDK: github.com/docker/docker v28.5.2+incompatible
- Containerd errdefs: github.com/containerd/errdefs v1.0.0
- Containerd errdefs pkg (split module providing pkg/errhttp): github.com/containerd/errdefs/pkg v0.3.0
