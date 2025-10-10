# OpenAPI Generation - Bazel Integration Complete ✅

## What Changed

The OpenAPI spec generation tool has been moved to `//libs/python/openapi_gen` and is now available as Bazel targets.

### Before

```bash
# Old location (deprecated)
python -m manman.src.host.openapi experience-api
```

### After

```bash
# New Bazel targets (specs defined alongside APIs)
bazel build //manman/src/host:all_api_specs
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api
```

## 📦 File Structure

```
libs/python/openapi_gen/
├── BUILD.bazel          # Tool library and binary (NEW)
├── __init__.py          # Package marker (NEW)
├── openapi_gen.py       # Core generation logic (MOVED from manman/src/host/openapi.py)
└── README.md           # Documentation (NEW)

manman/src/host/
└── BUILD.bazel         # OpenAPI spec targets (NEW - alongside API definitions)

tools/
└── generate_clients.py  # Updated to use new location

clients/
└── BUILD.bazel         # Updated with new dependencies

BAZEL_TARGETS.md        # Quick reference guide (NEW)
```

## 🎯 Available Bazel Targets

### OpenAPI Generation Tool (//libs/python/openapi_gen)

Reusable tool for generating OpenAPI specs:

```bash
# Run as CLI
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api

# Use as library in Python code
from libs.python.openapi_gen.openapi_gen import generate_openapi_spec
```

### API Spec Targets (//manman/src/host)

Generate specs as build artifacts (defined alongside the APIs):

```bash
# All specs
bazel build //manman/src/host:all_api_specs
ls bazel-bin/manman/src/host/*.json

# Individual specs
bazel build //manman/src/host:experience_api_spec
bazel build //manman/src/host:status_api_spec
bazel build //manman/src/host:worker_dal_api_spec
```

## 🚀 Quick Start

### Generate All Specs

```bash
# Build all OpenAPI specs
bazel build //manman/src/host:all_api_specs

# View generated specs
cat bazel-bin/manman/src/host/experience-api.json
cat bazel-bin/manman/src/host/status-api.json
cat bazel-bin/manman/src/host/worker-dal-api.json
```

### Generate and Use Clients

```bash
# Step 1: Generate specs (happens automatically in step 2, but shown for clarity)
bazel build //manman/src/host:all_api_specs

# Step 2: Generate Python clients
python tools/generate_clients.py --strategy=shared --build-wheel

# Step 3: Install and use
pip install clients/worker-dal-api-client/dist/*.whl
```

## 🔧 Integration Points

### Client Generation

The `tools/generate_clients.py` script now imports from the new location:

```python
from libs.python.openapi_gen.openapi_gen import generate_openapi_spec
```

### Bazel Dependencies

Added to `manman/src/host/BUILD.bazel`:

```starlark
# OpenAPI spec generation targets
genrule(
    name = "experience_api_spec",
    outs = ["experience-api.json"],
    cmd = """
        $(location //libs/python/openapi_gen:openapi_gen) experience-api --output-dir $(@D)
    """,
    tools = ["//libs/python/openapi_gen:openapi_gen"],
)
# ... similar for other APIs
```

## ✅ Benefits

1. **Bazel Native** - Generate specs as part of the build graph
2. **Cacheable** - Bazel caches generated specs
3. **Reusable** - Tool can be used by multiple packages
4. **Testable** - Can add validation rules
5. **Discoverable** - `bazel query //manman/src/host:*api_spec`
6. **CI/CD Ready** - Easy to integrate into build pipelines
7. **Co-located** - Spec targets live alongside the API definitions

## 📚 Documentation

- **[BAZEL_TARGETS.md](./BAZEL_TARGETS.md)** - Complete Bazel targets reference
- **[libs/python/openapi_gen/README.md](./libs/python/openapi_gen/README.md)** - Library documentation
- **[clients/README.md](./clients/README.md)** - Client generation guide
- **[manman/design/OPENAPI_SUMMARY.md](./manman/design/OPENAPI_SUMMARY.md)** - Complete implementation overview

## 🔄 Migration Notes

### Deprecated (but still works)

```bash
python -m manman.src.host.openapi experience-api
```

### Recommended

```bash
bazel build //manman/src/host:experience_api_spec
# Or
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api
```

### For Client Generation

The client generation script automatically uses the new location - no changes needed:

```bash
python tools/generate_clients.py --strategy=shared
```

## 🧪 Verify Installation

```bash
# Query available tool targets
bazel query //libs/python/openapi_gen:*

# Query available spec targets
bazel query //manman/src/host:*api_spec

# Should show:
# //manman/src/host:all_api_specs
# //manman/src/host:experience_api_spec
# //manman/src/host:status_api_spec
# //manman/src/host:worker_dal_api_spec

# Test generation
bazel build //manman/src/host:experience_api_spec

# Verify output
cat bazel-bin/manman/src/host/experience-api.json | jq .info.title
# Should output: "ManMan Experience API"
```

## 💡 Example Use Cases

### Use Case 1: CI/CD Pipeline

```yaml
# .github/workflows/openapi.yml
- name: Generate OpenAPI Specs
  run: bazel build //manman/src/host:all_api_specs

- name: Upload Artifacts
  uses: actions/upload-artifact@v3
  with:
    name: openapi-specs
    path: bazel-bin/manman/src/host/*.json
```

### Use Case 2: Documentation Generation

```starlark
# BUILD.bazel
genrule(
    name = "api_docs",
    srcs = ["//manman/src/host:all_api_specs"],
    outs = ["docs/index.html"],
    cmd = """
        # Generate HTML docs from OpenAPI specs
        openapi-generator-cli generate-html $(SRCS) -o $@
    """,
)
```

### Use Case 3: Development Workflow

```bash
# Terminal 1: Watch for API changes
ibazel build //manman/src/host:all_api_specs

# Terminal 2: Edit API
vim manman/src/host/api/experience/api.py

# Terminal 1: Automatically rebuilds specs
```

## 🎉 Summary

✅ **OpenAPI generation is now a Bazel target**  
✅ **Tool library in //libs/python/openapi_gen**  
✅ **Spec targets alongside APIs in //manman/src/host**  
✅ **Integrated with client generation**  
✅ **Fully documented**  
✅ **Backward compatible** (old method still works)  

**Next Steps:**
1. Try it: `bazel build //manman/src/host:all_api_specs`
2. View output: `ls bazel-bin/manman/src/host/`
3. Generate clients: `python tools/generate_clients.py --strategy=shared`
