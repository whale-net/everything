# Multi-Layer Container Images - Quick Start

## The Problem
Previously, all Python code was packaged into a single Docker layer:
```
Base → CA Certs → [EVERYTHING: Interpreter + Deps + Libs + App Code]
                   ↑ 200 MB - invalidated on ANY code change
```

## The Solution  
Now you can split dependencies into separate layers:
```
Base → CA Certs → [Pip Deps 150MB] → [Libs 40MB] → [App Code 5MB]
         ✓ cached     ✓ cached         ✓ cached      ✗ changed
```

Result: **40x improvement** in Docker transfer size for code changes!

## Usage

### Step 1: Create dependency layer
```python
py_library(
    name = "pip_deps_layer",
    deps = ["@pypi//fastapi", "@pypi//uvicorn"],
)
```

### Step 2: Use it in your app
```python
py_binary(
    name = "my-app",
    deps = [":pip_deps_layer"],
)
```

### Step 3: Configure layering
```python
release_app(
    name = "my-app",
    language = "python",
    domain = "demo",
    dep_layers = [
        {"name": "pip_deps", "targets": [":pip_deps_layer"]},
    ],
)
```

That's it! Your image now has separate layers for better caching.

## Benefits

- ✅ **40x faster** for app code changes
- ✅ **Backward compatible** - existing apps work unchanged
- ✅ **Simple API** - just one parameter
- ✅ **Explicit control** - you decide the layers
- ✅ **Cross-platform** - works with AMD64 and ARM64

## Examples

- `demo/hello_fastapi/BUILD.bazel` - FastAPI with pip deps layer
- `demo/hello_python/BUILD.bazel` - Python with internal libs layer

## Documentation

- **Complete Guide:** [`docs/LAYERED_CONTAINER_IMAGES.md`](docs/LAYERED_CONTAINER_IMAGES.md)
- **Implementation Details:** [`docs/IMPLEMENTATION_SUMMARY.md`](docs/IMPLEMENTATION_SUMMARY.md)
- **Agent Guide:** [`AGENTS.md`](AGENTS.md) (Multi-Layer Docker Caching section)

## Testing

Validate implementation:
```bash
python3 tools/validate_layering.py
```

Build with layering:
```bash
bazel build //demo/hello_fastapi:hello-fastapi_image_base
```

Verify layers:
```bash
bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_arm64
docker history demo-hello-fastapi:latest
```

## Migration

No changes required for existing apps. To opt-in:

1. Extract deps into py_library layers
2. Update app to depend on layers
3. Add `dep_layers` to `release_app()`

See [`docs/LAYERED_CONTAINER_IMAGES.md`](docs/LAYERED_CONTAINER_IMAGES.md) for detailed migration guide.
