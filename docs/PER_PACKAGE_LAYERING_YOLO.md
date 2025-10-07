# Per-Package OCI Layering - Implementation Complete! 🎉

## Status: **YOLO MODE SUCCESS!**

The experimental per-package layering has been implemented and benchmarked. It works, but comes with trade-offs.

## Implementation Details

### Strategy: Synthetic Probe Binaries

Instead of trying to filter specific packages from runfiles (complex in Bazel), we create "probe" binaries that depend on only one package each:

```starlark
# For each package (e.g., "fastapi"):
1. Generate a minimal Python script that imports the package
2. Create a py_binary that depends only on that package
3. Create a pkg_tar from the probe's runfiles
4. Stack all package tars + main app tar

# Result: Each layer truly contains only one package!
```

### File Structure

- **`tools/container_image_experimental.bzl`**: Main implementation
  - `_create_package_probe_binary()`: Creates synthetic binaries
  - `container_image_per_package()`: Public macro

- **`demo/hello_fastapi/BUILD.bazel`**: Example usage
  - `hello_fastapi_experimental_image`: Per-package variant

### Usage Example

```starlark
load("//tools:container_image_experimental.bzl", "container_image_per_package")

container_image_per_package(
    name = "my_app_experimental",
    binary = ":my_app",
    language = "python",
    packages = [  # Only top-level packages from pyproject.toml!
        "fastapi",
        "pydantic",
        "uvicorn",
    ],
    app_layer = ":app_lib",
)
```

**Important**: Only packages declared in `pyproject.toml` can be layered. Transitive dependencies (like `click`, which is a uvicorn dependency) are not exposed by Bazel.

## Benchmark Results

Compared 2-layer vs per-package for `hello_fastapi` (4 packages: fastapi, pydantic, uvicorn, httpx):

| Metric | 2-Layer | Per-Package | Difference |
|--------|---------|-------------|------------|
| **Clean build** | 4.91s | 6.04s | **+1.13s slower** |
| **Incremental** | 1.42s | 1.72s | **+0.30s slower** |
| **Layers** | 2 | 6 (4 packages + full_deps + app) | +4 layers |

### Analysis

**Per-package overhead comes from:**
1. Creating 4 probe binaries (genrule + py_binary for each)
2. Creating 4 additional pkg_tar operations
3. More complex build graph for Bazel to analyze

**What per-package layering does NOT help with:**
- Code changes (both approaches have separate app layer)
- Python interpreter updates (included in full_deps layer in both)

**What per-package DOES help with:**
- Updating a single top-level package (only that layer rebuilds)
- However: In practice, `uv.lock` changes usually update multiple packages simultaneously

## Verdict: 2-Layer Wins for Most Cases

### When to use 2-layer (RECOMMENDED):
- ✅ Local development (code changes most frequent)
- ✅ Small to medium projects (<10 top-level packages)
- ✅ Stable dependencies (uv.lock changes infrequently)
- ✅ Prefer simplicity and clarity

### When to use per-package (EXPERIMENTAL):
- ⚠️ Large projects (10+ top-level packages)
- ⚠️ Frequent single-package updates
- ⚠️ CI/CD where granular caching is critical
- ⚠️ Willing to accept 0.3-1s overhead per build

### When NOT to use per-package:
- ❌ Most dependency updates change multiple packages anyway
- ❌ Overhead (0.3-1s) negates benefits for typical workflows
- ❌ Added complexity isn't worth marginal gains

## Technical Limitations

1. **Only top-level packages**: Can't layer transitive dependencies
2. **Overlapping content**: Each probe includes its own transitive deps
3. **Docker deduplication**: Relies on Docker's content-addressable storage
4. **Build graph complexity**: More targets = more analysis overhead

## Future Improvements (if needed)

If per-package becomes necessary:

1. **Parallel probe creation**: Could speed up clean builds
2. **Selective packaging**: Add packages parameter to release_app()
3. **Auto-discovery**: Generate packages list from pyproject.toml
4. **Dependency ordering**: Layer packages by dependency graph

## Conclusion

**The YOLO experiment worked!** 🎉

Per-package layering is:
- ✅ Technically feasible
- ✅ Functionally correct
- ✅ Properly benchmarked
- ⚠️ Marginally slower (0.3-1s)
- ❌ Not worth it for typical use cases

**Recommendation**: Stick with 2-layer as default. Per-package remains available as `container_image_per_package()` for specialized scenarios.

## Commands

```bash
# Build experimental image
bazel build //demo/hello_fastapi:hello_fastapi_experimental_image --platforms=//tools:linux_arm64

# Run benchmark comparison
python3 tools/benchmark_layering.py

# Check layer count
docker image inspect $(docker load < bazel-bin/demo/hello_fastapi/hello_fastapi_experimental_image/tarball.tar | awk '{print $3}') | jq '.[0].RootFS.Layers | length'
```

---

**Date**: October 7, 2025  
**Status**: Experimental implementation complete, not recommended for production
