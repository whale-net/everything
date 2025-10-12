# Layered Container Images for Better Caching

## Problem Statement

By default, container images created by `release_app` package all Python code (app code + dependencies) into a single Docker layer. This is convenient but has poor caching characteristics:

- **Any code change invalidates the entire layer**, including 3rd party pip dependencies
- Rebuilding and re-downloading images takes longer than necessary
- CI/CD pipelines waste time re-pulling unchanged dependencies

## Solution: Explicit Dependency Layers

The container image system now supports **explicit dependency layers** through the `dep_layers` parameter. This allows you to separate:

1. **External pip dependencies** (rarely change)
2. **Internal shared libraries** (change occasionally)  
3. **Application code** (changes frequently)

Each layer group is cached independently, so changes to app code don't invalidate pip dependency layers.

## Usage

### Basic Setup

1. **Create a py_library for each dependency group:**

```python
# Layer 1: External pip dependencies
py_library(
    name = "pip_deps_layer",
    deps = [
        "@pypi//:fastapi",
        "@pypi//:uvicorn",
        "@pypi//:pydantic",
    ],
)

# Layer 2: Internal shared libs (optional)
py_library(
    name = "internal_deps_layer",
    deps = ["//libs/python"],
)

# Your app library depends on the layers
py_library(
    name = "main_lib",
    srcs = ["main.py"],
    deps = [
        ":pip_deps_layer",
        ":internal_deps_layer",
    ],
)
```

2. **Configure release_app with dep_layers:**

```python
release_app(
    name = "my-app",
    language = "python",
    domain = "demo",
    description = "My application with optimized layer caching",
    dep_layers = [
        {
            "name": "pip_deps",
            "targets": [":pip_deps_layer"],
        },
        {
            "name": "internal_libs",
            "targets": [":internal_deps_layer"],
        },
    ],
)
```

### Layer Ordering

Layers are added to the Docker image in the order specified:

1. **Base image** (Ubuntu + system packages)
2. **CA certificates layer** (automatically added)
3. **dep_layers** (in the order you specify)
4. **Application binary layer** (automatically added last)

**Best practice:** Order layers from least to most frequently changed:
- Pip dependencies first (change when requirements.txt changes)
- Internal libs second (change when shared code changes)
- App code last (changes most frequently)

## Example: hello-fastapi

See `demo/hello_fastapi/BUILD.bazel` for a complete example:

```python
# Step 1: Create dependency layer
py_library(
    name = "pip_deps_layer",
    deps = [
        "@pypi//:fastapi",
        "@pypi//:uvicorn",
    ],
)

# Step 2: Use it in your app
py_library(
    name = "main_lib",
    srcs = ["main.py"],
    deps = [":pip_deps_layer"],
)

py_binary(
    name = "hello-fastapi",
    srcs = ["main.py"],
    deps = [":main_lib"],
)

# Step 3: Configure layers in release_app
release_app(
    name = "hello-fastapi",
    language = "python",
    domain = "demo",
    description = "FastAPI with multi-layer optimization",
    dep_layers = [
        {"name": "pip_deps", "targets": [":pip_deps_layer"]},
    ],
)
```

## Benefits

### Cache Hit Improvements

**Before (single layer):**
- Change app code → Entire layer invalidated → All dependencies re-packaged
- Docker pull: Download entire layer (hundreds of MB)

**After (multi-layer):**
- Change app code → Only app layer invalidated → Pip deps cached
- Docker pull: Download only app layer (a few MB)

### CI/CD Performance

- **Faster builds:** Bazel caches pkg_tar for unchanged layers
- **Faster pulls:** Docker caches unchanged dependency layers
- **Faster pushes:** Only changed layers pushed to registry

### Dependency Management

- Clear separation of external vs internal dependencies
- Easy to see which dependencies are in which layer
- Better dependency hygiene

## Migration Guide

Existing apps work without changes - the single-layer behavior is the default.

To opt-in to multi-layer caching:

1. **Identify your pip dependencies** by looking at `deps` in py_library/py_binary
2. **Extract them into a pip_deps_layer py_library**
3. **Update your app's deps** to depend on the layer
4. **Add dep_layers parameter** to release_app
5. **Test the build:** `bazel build //your/app:app_image_base`

### Template

```python
# Before: Single layer
py_binary(
    name = "my-app",
    srcs = ["main.py"],
    deps = [
        "@pypi//:package1",
        "@pypi//:package2",
        "//libs/python",
    ],
)

# After: Multi-layer
py_library(
    name = "pip_deps_layer",
    deps = [
        "@pypi//:package1",
        "@pypi//:package2",
    ],
)

py_library(
    name = "internal_deps_layer",
    deps = ["//libs/python"],
)

py_binary(
    name = "my-app",
    srcs = ["main.py"],
    deps = [
        ":pip_deps_layer",
        ":internal_deps_layer",
    ],
)

release_app(
    name = "my-app",
    # ... other params ...
    dep_layers = [
        {"name": "pip_deps", "targets": [":pip_deps_layer"]},
        {"name": "internal_libs", "targets": [":internal_deps_layer"]},
    ],
)
```

## Technical Details

### Implementation

The `container_image()` function creates separate `pkg_tar` targets for each layer:

```starlark
# For each dep_layer:
pkg_tar(
    name = name + "_deplayer_0_pip_deps",
    deps = [":pip_deps_layer"],
    package_dir = "/app",
    include_runfiles = True,
)

# Final app layer:
pkg_tar(
    name = name + "_app_layer",
    srcs = [binary],
    package_dir = "/app",
    include_runfiles = True,
)
```

These are then composed into the final image:

```starlark
oci_image(
    tars = [
        "//tools/cacerts:cacerts",  # CA certs
        ":name_deplayer_0_pip_deps", # Pip deps
        ":name_app_layer",           # App code
    ],
)
```

### File Deduplication

If a file appears in multiple layers (e.g., transitive deps), Docker automatically deduplicates it. The file from the later layer wins, which is the correct behavior since later layers represent "more specific" dependencies.

### Bazel Caching

Each `pkg_tar` is cached independently by Bazel. If a dependency layer hasn't changed, Bazel reuses the cached tar, saving build time.

### OCI Layer Digests

Docker/OCI use content-addressable storage. If a layer's content hasn't changed (even after a rebuild), the digest is the same and registries/Docker cache it.

## Best Practices

1. **Group dependencies logically:**
   - External packages (@pypi) in one layer
   - Internal shared libs (//libs) in another
   - App-specific code in the final layer

2. **Order by change frequency:**
   - Least frequently changed first
   - Most frequently changed last

3. **Don't over-layer:**
   - Too many layers → Diminishing returns
   - 2-3 layers is usually optimal

4. **Test your layering:**
   - Build twice: `bazel build //app:app_image_base`
   - Second build should be faster (cache hits)
   - Check Docker image: `docker history <image>` to see layers

5. **Monitor layer sizes:**
   - Use `docker images` to check layer sizes
   - Large app layer = good caching benefit
   - Large dep layer = one-time cost, cached after

## Troubleshooting

### "Target not found" errors

Make sure your py_library targets are visible:

```python
py_library(
    name = "pip_deps_layer",
    deps = [...],
    visibility = ["//visibility:private"],  # OK for same package
)
```

### Duplicate files in image

This is normal! Docker deduplicates them. If you want to verify, inspect the image:

```bash
bazel run //app:app_image_load --platforms=//tools:linux_arm64
docker run --rm -it app:latest sh
# Check /app directory
```

### Build is slower

- First build is slower (creating all layers)
- Subsequent builds should be faster
- The benefit is in Docker cache hits, not build time

### Cache not working

Check:
1. Are layers actually different? (change app code, not deps)
2. Is Docker caching enabled? (`docker system df` to check)
3. Are you pulling from registry? (layers cached there too)

## Future Improvements

Potential enhancements:

1. **Automatic layer detection:** Analyze py_binary deps and create layers automatically
2. **Python interpreter layer:** Separate Python runtime from pip packages
3. **Per-package layers:** Create one layer per pip package for ultra-fine caching
4. **Build analysis:** Report cache hit rates and layer sizes

## References

- [Docker layer caching documentation](https://docs.docker.com/build/cache/)
- [OCI image specification](https://github.com/opencontainers/image-spec)
- [Bazel rules_pkg documentation](https://github.com/bazelbuild/rules_pkg)
- Example: `demo/hello_fastapi/BUILD.bazel`
