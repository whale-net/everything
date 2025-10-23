# Container Image Layer Caching with Bazel

This document explains how the Everything monorepo leverages Bazel's incremental caching for efficient container image builds.

## Architecture Overview

Python container images are built using **4 independent layers** with explicit Bazel targets:

```
┌─────────────────────────────────────────┐
│  1. Base Ubuntu Image                   │  ← From upstream
│     (~100MB)                             │
└─────────────────────────────────────────┘
           ↓
┌─────────────────────────────────────────┐
│  2. CA Certificates Layer               │  ← //tools/cacerts:cacerts
│     //tools/cacerts:cacerts              │     Cached: Package version
│     (~127KB)                             │
└─────────────────────────────────────────┘
           ↓
┌─────────────────────────────────────────┐
│  3. Python Interpreter Layer            │  ← {app}_python_layer
│     {app}_python_layer                   │     Cached: Python version + platform
│     (~240-375MB stripped)                │
└─────────────────────────────────────────┘
           ↓
┌─────────────────────────────────────────┐
│  4. Dependencies Layer                   │  ← {app}_deps_layer
│     {app}_deps_layer                     │     Cached: uv.lock + wheel artifacts
│     (~10KB-100MB)                        │
└─────────────────────────────────────────┘
           ↓
┌─────────────────────────────────────────┐
│  5. App Code Layer                       │  ← {app}_app_layer
│     {app}_app_layer                      │     Cached: Source files in _main
│     (~60KB-10MB)                         │     Changes MOST frequently
└─────────────────────────────────────────┘
```

## Bazel Caching Behavior

Each layer is a separate Bazel target with its own dependency graph. Bazel caches each layer based on its **transitive inputs**.

### Layer 1: CA Certificates
**Target**: `//tools/cacerts:cacerts`

**Cached based on**:
- Ubuntu ca-certificates package version
- Extraction script

**Rebuilt when**:
- Base certificate package is updated
- Extraction logic changes

**Typical rebuild frequency**: Quarterly

---

### Layer 2: Python Interpreter
**Target**: `//demo/hello_fastapi:hello-fastapi_image_base_python_layer`

**Cached based on**:
- Python toolchain version (from rules_python)
- Target platform (`--platforms` flag)
- `strip_python.sh` script

**Rebuilt when**:
- Python version changes (e.g., 3.13.0 → 3.13.1)
- Target platform changes (amd64 ↔ arm64)
- Stripping script changes

**Typical rebuild frequency**: Monthly (Python patch releases)

**Size**: 240-375MB (varies by platform after stripping)

---

### Layer 3: Third-Party Dependencies
**Target**: `//demo/hello_fastapi:hello-fastapi_image_base_deps_layer`

**Cached based on**:
- `uv.lock` content (lock file hash)
- Resolved wheel artifacts for target platform
- `strip_python.sh` script

**Rebuilt when**:
- Dependencies added/updated/removed in `uv.lock`
- Wheel versions change for target platform
- Stripping script changes

**Typical rebuild frequency**: Weekly (during active development)

**Size**: 10KB-100MB depending on app dependencies

---

### Layer 4: Application Code
**Target**: `//demo/hello_fastapi:hello-fastapi_image_base_app_layer`

**Cached based on**:
- App source files (`*.py` in app directory)
- Local library files (`//libs/python`)
- Binary metadata and MANIFEST files

**Rebuilt when**:
- Any `.py` file changes in the app
- Any local library code changes
- Generated code changes (OpenAPI clients)

**Typical rebuild frequency**: Every commit during development

**Size**: 60KB-10MB depending on app

---

## Caching Demonstration

Here's what happens when you modify app code:

```bash
# Initial build (all layers built)
$ bazel build //demo/hello_fastapi:hello-fastapi_image_base --platforms=//tools:linux_x86_64
INFO: Executing genrule //demo/hello_fastapi:hello-fastapi_image_base_python_layer
INFO: Executing genrule //demo/hello_fastapi:hello-fastapi_image_base_deps_layer  
INFO: Executing genrule //demo/hello_fastapi:hello-fastapi_image_base_app_layer
INFO: Build completed successfully

# Modify app source code
$ echo "# comment" >> demo/hello_fastapi/main.py

# Rebuild (only app layer rebuilds!)
$ bazel build //demo/hello_fastapi:hello-fastapi_image_base --platforms=//tools:linux_x86_64
INFO: Executing genrule //demo/hello_fastapi:hello-fastapi_image_base_app_layer
INFO: 6 processes: 2 action cache hit, 3 internal, 1 linux-sandbox
INFO: Build completed successfully
```

**Result**: Only the 60KB app layer rebuilds. The 240MB Python interpreter and 33MB dependencies layers are served from cache!

## Benefits

### 1. **Local Development Speed**
- Typical app code change: **Only 1 layer rebuilds** (~60KB)
- Python interpreter: Cached until Python version bump
- Dependencies: Cached until `uv.lock` changes
- Build time: **Seconds** instead of minutes

### 2. **CI/CD Efficiency**
- Most PRs only modify app code → 99% of image cached
- Dependency updates → 75% of image cached (Python + CA certs)
- Python version updates → 25% of image cached (CA certs only)

### 3. **Registry Bandwidth**
- Docker/OCI registries only transfer **changed layers**
- App code change: Upload ~60KB instead of 350MB+
- Pull operations: Download only changed layers

### 4. **Remote Cache Benefits**
With Bazel remote caching, layers are shared across:
- All developers on your team
- CI workers
- Different branches (if compatible)

## Advanced Optimization Options

### Option 1: Split Dependencies Further

You could split the dependencies layer into "stable" and "volatile" deps:

```python
# In container_image.bzl
# Stable dependencies (rarely change)
stable_deps_layer = [
    "numpy", "pandas", "pydantic", "sqlalchemy"
]

# Volatile dependencies (change frequently)  
volatile_deps_layer = [
    "fastapi", "uvicorn", "httpx"
]
```

**Pros**:
- Even finer-grained caching
- Stable scientific computing stack cached separately

**Cons**:
- More complex logic
- More layers = slightly larger image metadata
- Marginal benefit for most apps

**Recommendation**: Only needed for apps with 50+ dependencies

---

### Option 2: Dependency on Lock File Only

Currently, layers depend on the full binary, which includes all transitive deps. We could create targets that depend **directly** on `uv.lock`:

```python
# Theoretical optimization
pkg_tar(
    name = name + "_deps_from_lock",
    srcs = ["@pip_deps//..."],  # Direct dependency
    data = ["//:uv.lock"],       # Explicit lock file dependency
    # ...
)
```

**Pros**:
- More explicit dependency graph
- Could parallelize layer extraction

**Cons**:
- Requires custom Bazel rules
- Complex implementation
- Bazel already handles this efficiently via content hashing

**Recommendation**: Current approach is cleaner and sufficient

---

### Option 3: Remote Cache Configuration

The biggest win comes from using Bazel's remote cache:

```bash
# .bazelrc
build --remote_cache=grpc://cache.example.com:9092
build --remote_upload_local_results=true

# Or use Google Cloud Storage
build --remote_cache=https://storage.googleapis.com/my-bucket
```

**Benefits**:
- Share cached layers across all developers
- Share between CI jobs
- Persistent cache across machines

**Setup**: See [Bazel Remote Caching Docs](https://bazel.build/remote/caching)

---

### Option 4: Build Without the Bytes (`--remote_download_minimal`)

This is a Bazel optimization that **avoids downloading intermediate build artifacts** when using remote cache.

#### How It Works

When you run a normal build:
```bash
bazel build //demo/hello_fastapi:hello-fastapi_image_base
```

Bazel does:
1. ✅ Checks remote cache for each action (genrule, pkg_tar, etc.)
2. ✅ Downloads **all output files** from cache to local disk
3. ✅ Uses them to build the final target

With `--remote_download_minimal`:
```bash
bazel build //demo/hello_fastapi:hello-fastapi_image_base \
    --remote_download_minimal
```

Bazel does:
1. ✅ Checks remote cache for each action
2. ❌ **Does NOT download intermediate files** (e.g., layer tars)
3. ✅ Only downloads the **final requested target**

#### What Does This Mean for Image Layers?

**Without the flag** (default):
```
Remote Cache                     Local Disk
────────────────────────────────────────────
python_layer.tar (240MB)    →    Downloaded ✓
deps_layer.tar (33MB)       →    Downloaded ✓
app_layer.tar (60KB)        →    Downloaded ✓
final_image (metadata)      →    Downloaded ✓

Total downloaded: 273MB
```

**With `--remote_download_minimal`**:
```
Remote Cache                     Local Disk
────────────────────────────────────────────
python_layer.tar (240MB)    →    Skipped! (kept in remote cache)
deps_layer.tar (33MB)       →    Skipped! (kept in remote cache)
app_layer.tar (60KB)        →    Skipped! (kept in remote cache)
final_image (metadata)      →    Downloaded ✓

Total downloaded: ~1KB (just metadata)
```

#### Important: This Only Affects Local Disk

The **OCI image layers are still built correctly** and pushed to the registry! The flag only controls what Bazel downloads to your local filesystem.

**Build flow with remote cache**:
1. Bazel checks: "Do I have python_layer.tar in remote cache?" → Yes
2. Bazel checks: "Do I have deps_layer.tar in remote cache?" → Yes  
3. Bazel checks: "Do I have app_layer.tar in remote cache?" → No, app code changed
4. Bazel **builds app_layer.tar** using the cached references
5. Bazel **assembles final image** using all layer references (from cache or newly built)
6. With `--remote_download_minimal`: Bazel never downloads the 240MB Python layer to your laptop!

#### Use Cases

**✅ Perfect for**:
- **CI/CD pipelines** that just push images to registry
- **Remote execution** where build happens on cloud workers
- **Developers with slow internet** who don't need intermediate artifacts locally

**❌ Not useful for**:
- `oci_load` targets (loading image into local Docker)
- Debugging intermediate layers
- When you need to inspect layer contents locally

#### Example CI Workflow

```yaml
# .github/workflows/release.yml
- name: Build and Push Images
  run: |
    bazel build //demo/hello_fastapi:hello-fastapi_image_push \
      --remote_cache=${{ secrets.BAZEL_CACHE_URL }} \
      --remote_download_minimal \
      --remote_upload_local_results=true
    
    # Image is built and ready to push
    # But we never downloaded the 240MB Python layer!
    bazel run //demo/hello_fastapi:hello-fastapi_image_push
```

**Result**: 
- Build uses cached layers from remote cache
- Only changed layers are rebuilt
- Nothing downloaded to CI worker
- Final image pushed to registry
- **Saves bandwidth and disk I/O**

#### Comparison with Other Modes

Bazel has three remote download modes:

| Mode | Downloads | Use Case |
|------|-----------|----------|
| `all` | Everything from remote cache | Default, safest |
| `toplevel` | Only final requested targets | Good balance |
| `minimal` | Only targets explicitly requested via command line | Maximum savings, CI/CD |

**For our image builds**:

```bash
# Normal development (inspect layers locally)
bazel build //demo/hello_fastapi:hello-fastapi_image_base

# CI build and push (never need local layers)
bazel build //demo/hello_fastapi:hello-fastapi_image_push \
    --remote_download_minimal
```

#### Caveats

1. **Can't inspect intermediate artifacts**: If a build fails, you won't have the intermediate files locally for debugging
2. **Not useful with `oci_load`**: You need the actual image tar locally to load into Docker
3. **Requires remote cache**: This flag is useless without `--remote_cache` configured

#### Performance Impact

**Typical image build in CI with remote cache**:

Without `--remote_download_minimal`:
```
Build time: 2 seconds
Download time: 30 seconds (downloading 273MB of cached layers)
Total: 32 seconds
```

With `--remote_download_minimal`:
```
Build time: 2 seconds
Download time: 0 seconds (metadata only)
Total: 2 seconds
```

**Savings**: 15x faster in CI when everything is cached!

---

## Measuring Cache Performance

### Check Cache Hit Rate

```bash
# Build with execution log
bazel build //demo/hello_fastapi:hello-fastapi_image_base \
    --execution_log_json_file=execution.log

# Count cache hits
jq '[.[] | select(.remoteCacheHit == true)] | length' execution.log
```

### Analyze What Changed

```bash
# See why a target was rebuilt
bazel query --output=build //demo/hello_fastapi:hello-fastapi_image_base_app_layer

# Check action keys (for cache debugging)
bazel aquery //demo/hello_fastapi:hello-fastapi_image_base_app_layer
```

### Invalidate Cache Manually

```bash
# Clean specific target
bazel clean --expunge_async //demo/hello_fastapi:hello-fastapi_image_base_app_layer

# Force rebuild
bazel build //demo/hello_fastapi:hello-fastapi_image_base --nocache_test_results
```

## Best Practices

### 1. **Stable Dependencies**
Pin dependencies in `uv.lock` to avoid unnecessary rebuilds:
```bash
uv lock  # Regenerate lock file only when needed
```

### 2. **Clean Commits**
Avoid committing temporary changes that invalidate app layer cache:
- Remove debug print statements
- Don't commit commented-out code
- Use `.gitignore` for generated files

### 3. **Parallel Builds**
Bazel automatically parallelizes layer building:
```bash
# Build with more parallelism
bazel build //... --jobs=8
```

### 4. **Remote Cache for CI**
Always use remote cache in CI:
```yaml
# .github/workflows/ci.yml
- name: Build Images
  run: bazel build //... --remote_cache=${{ secrets.BAZEL_CACHE_URL }}
```

## Troubleshooting

### "Layer always rebuilds"

Check what inputs changed:
```bash
bazel query "deps(//demo/hello_fastapi:hello-fastapi_image_base_python_layer)" | \
    grep -v "@bazel_tools"
```

### "Build is slow even with cache"

Enable detailed timing:
```bash
bazel build //... --profile=profile.json
bazel analyze-profile profile.json
```

### "Remote cache not working"

Verify authentication and connectivity:
```bash
bazel info | grep remote_cache
bazel build //... --remote_cache=grpc://cache.example.com:9092 --verbose_failures
```

## Summary

The current implementation provides **excellent caching** with:
- ✅ Independent layer targets
- ✅ Minimal rebuilds on app changes
- ✅ Bazel action caching for all layers
- ✅ Parallelizable layer extraction
- ✅ Compatible with remote caching

For most use cases, the current architecture is **optimal**. Advanced optimizations like splitting dependencies or direct lock file dependencies provide diminishing returns compared to their complexity.

**Focus instead on**:
1. Setting up remote caching (biggest impact!)
2. Keeping `uv.lock` stable
3. Using `--remote_download_minimal` in CI
4. Monitoring cache hit rates

---

**Related Documentation**:
- [BUILDING_CONTAINERS.md](./BUILDING_CONTAINERS.md) - Container build basics
- [tools/bazel/container_image.bzl](../tools/bazel/container_image.bzl) - Implementation
- [Bazel Caching](https://bazel.build/remote/caching) - Official docs
