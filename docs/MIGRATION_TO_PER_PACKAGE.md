# Migration to Per-Package Layering (Complete)

**Date**: 2025-01-11  
**Status**: ✅ **COMPLETE**

## Executive Summary

We successfully converted the entire codebase from 2-layer OCI image builds to per-package layering as the default strategy. This prioritizes granular caching over raw build speed (~0.3s slower, but each pip package gets its own layer).

## What Changed

### Core Infrastructure

1. **`tools/container_image.bzl`** - Completely rewritten
   - Added `_create_package_probe_binary()` helper function
   - `container_image()` now accepts `packages` parameter
   - Creates N package layers + full_deps layer + app layer
   - Uses synthetic "probe" binaries to isolate dependencies per package
   - `multiplatform_image()` passes packages through

2. **`tools/release.bzl`** - Updated public API
   - Added `packages` parameter to `release_app()` macro
   - Updated documentation with per-package example
   - Passes packages through to `multiplatform_image()`

3. **`tools/container_image_experimental.bzl`** - DELETED
   - Was the original experimental implementation
   - No longer needed since per-package is now the default

### Application Updates

All Python demo apps updated with `packages` parameter:

1. **hello_fastapi**
   ```python
   packages = ["fastapi", "pydantic", "uvicorn", "httpx"]
   ```
   - Removed experimental target
   - Added `app_layer = ":main_lib"`

2. **hello_internal_api**
   ```python
   packages = ["fastapi", "uvicorn"]
   ```

3. **hello_python**
   ```python
   packages = ["pytest"]
   ```

4. **hello_worker**
   ```python
   packages = ["pytest"]
   ```

5. **hello_job**
   ```python
   packages = ["pytest"]
   ```

6. **hello_world_test**
   ```python
   packages = ["pytest"]
   ```

7. **hello_go** - No changes needed (Go doesn't use Python packages)

## Technical Details

### How Per-Package Layering Works

1. **Synthetic Probe Binaries**: For each package, we create a minimal `py_binary`:
   ```python
   genrule(
       name = f"{package_name}_probe_script",
       outs = [f"{package_name}_probe.py"],
       cmd = "echo 'import sys; sys.exit(0)' > $@",
   )
   
   py_binary(
       name = f"{package_name}_probe",
       srcs = [f":{package_name}_probe_script"],
       deps = [f"@pypi//:{package_name}"],
   )
   ```

2. **Layer Creation**: Each probe's runfiles become a layer:
   ```python
   pkg_tar(
       name = f"{package_name}_layer",
       deps = [f":{package_name}_probe"],
   )
   ```

3. **Layer Stacking**:
   - Package layers (fastapi, pydantic, uvicorn, httpx, etc.)
   - Full deps layer (all dependencies together - fallback)
   - App layer (your code)

4. **Docker Caching**: Docker caches each layer independently by content hash
   - Change fastapi version? Only fastapi layer rebuilds
   - Change app code? Only app layer rebuilds
   - All other layers come from cache

### Limitations

- Only **top-level packages** (declared in `pyproject.toml`) can be layered
- Transitive dependencies (like `click` from `uvicorn`) not exposed by `@pypi`
- This is fine - we focus on packages we explicitly manage

## Performance Impact

From benchmarking (`tools/benchmark_layering.py`):

- **2-layer approach**: ~1.4s incremental builds
- **Per-package approach**: ~1.7s incremental builds (+0.3s)

**Trade-off**: We accepted 0.3s slower builds for more granular caching:
- Smaller layers mean less data to rebuild/push
- Better cache hit rates when only one package changes
- More efficient CI/CD pipelines

## Migration Steps Taken

1. ✅ Copied per-package implementation from experimental to main `container_image.bzl`
2. ✅ Updated `multiplatform_image()` signature
3. ✅ Updated `release_app()` public API
4. ✅ Updated all 6 Python demo apps with packages lists
5. ✅ Verified Go app still works (no changes needed)
6. ✅ Deleted `tools/container_image_experimental.bzl`
7. ✅ Tested all builds successfully

## Testing Results

All apps build successfully:
```bash
# Python apps with per-package layering
bazel build //demo/hello_fastapi:hello_fastapi_image --platforms=//tools:linux_arm64
bazel build //demo/hello_internal_api:hello_internal_api_image --platforms=//tools:linux_arm64
bazel build //demo/hello_python:hello_python_image --platforms=//tools:linux_arm64
bazel build //demo/hello_worker:hello_worker_image --platforms=//tools:linux_arm64
bazel build //demo/hello_job:hello_job_image --platforms=//tools:linux_arm64
bazel build //demo/hello_world_test:hello_world_test_image --platforms=//tools:linux_arm64

# Go app (no packages parameter needed)
bazel build //demo/hello_go:hello_go_image --platforms=//tools:linux_arm64
```

All builds completed successfully. ✅

## Future Considerations

### For New Python Apps

When creating a new Python app, add the `packages` parameter to `release_app()`:

```python
release_app(
    name = "my_app",
    language = "python",
    packages = ["fastapi", "sqlalchemy", "redis"],  # List top-level packages
    app_layer = ":main_lib",
    # ... other params
)
```

**How to identify packages**:
1. Look at your `py_library` deps: `@pypi//:packagename`
2. Only include **direct** dependencies (not transitive)
3. Only packages declared in `pyproject.toml` work

### For Go Apps

No changes needed - Go apps don't use the `packages` parameter.

### Reverting to 2-Layer

If you need to revert (e.g., performance regression), just remove the `packages` parameter:

```python
release_app(
    name = "my_app",
    language = "python",
    # packages = ["fastapi", "pydantic"],  # Comment this out
    app_layer = ":main_lib",
)
```

The code automatically falls back to 2-layer when `packages` is `None`.

## Related Documentation

- `docs/LAYERING_DECISION.md` - Original decision document (now outdated)
- `docs/PER_PACKAGE_LAYERING_YOLO.md` - Technical implementation details
- `tools/benchmark_layering.py` - Benchmarking framework

## Conclusion

The migration is complete! Per-package layering is now the default for all Python apps. This provides:

✅ More granular caching per pip package  
✅ Smaller layers for better cache efficiency  
✅ Better CI/CD performance when dependencies change incrementally  
⚠️ ~0.3s slower builds (acceptable trade-off)

**User verdict**: "YOLO" - prioritizing granular caching over speed.
