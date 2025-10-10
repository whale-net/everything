# OpenAPI Generation Library

This library provides tools for generating OpenAPI specifications from FastAPI applications without requiring environment dependencies (database, message queue, etc.).

**Note:** This is the **tool library**. The actual Bazel targets for generating API specs are defined alongside the APIs themselves in `//manman/src/host:BUILD.bazel`.

## 📦 Structure

```
libs/python/openapi_gen/
├── BUILD.bazel          # Tool library and binary
├── __init__.py          # Package marker
├── openapi_gen.py       # Core generation logic
└── README.md           # This file

manman/src/host/
└── BUILD.bazel          # Actual OpenAPI spec generation targets
```

## 🎯 Usage

### Generate API Specs (Recommended)

The OpenAPI spec generation targets are defined where the APIs are:

```bash
# Generate all OpenAPI specs
bazel build //manman/src/host:all_api_specs

# View generated specs
ls bazel-bin/manman/src/host/*.json

# Or via convenience alias
bazel build //clients:generate_specs
```

Generate individual API specs:

```bash
# Experience API
bazel build //manman/src/host:experience_api_spec

# Status API
bazel build //manman/src/host:status_api_spec

# Worker DAL API
bazel build //manman/src/host:worker_dal_api_spec
```

### As a CLI Tool

Run directly via Bazel:

```bash
# Generate experience API spec
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api

# Generate with custom output directory
bazel run //libs/python/openapi_gen:openapi_gen -- status-api --output-dir ./my-specs

# Help
bazel run //libs/python/openapi_gen:openapi_gen -- --help
```

Run directly via Python (from project root):

```bash
python -m libs.python.openapi_gen.openapi_gen experience-api
python -m libs.python.openapi_gen.openapi_gen status-api -o ./custom-output
```

### As a Python Library

Import and use in your Python code:

```python
from pathlib import Path
from libs.python.openapi_gen.openapi_gen import generate_openapi_spec
from manman.src.host.api.experience import create_app

# Create FastAPI app
app = create_app()

# Generate spec
spec_path = generate_openapi_spec(
    fastapi_app=app,
    service_name="experience-api",
    output_dir=Path("./specs")
)

print(f"Spec saved to: {spec_path}")
```

## 🔧 Integration with Client Generation

The client generation script uses this library:

```bash
# Generate clients (automatically generates specs first)
python tools/generate_clients.py --strategy=shared
```

## 📝 Available APIs

The following ManMan APIs can be generated:

- `experience-api` - Main worker-facing API for game server management
- `status-api` - Read-only status queries
- `worker-dal-api` - Data access layer for worker operations

## 🏗️ Bazel Targets Reference

### Tool Library (this package)

| Target | Type | Description |
|--------|------|-------------|
| `//libs/python/openapi_gen:openapi_gen_lib` | `py_library` | Library for use in other Python code |
| `//libs/python/openapi_gen:openapi_gen` | `py_binary` | CLI tool for generating specs |

### API Spec Targets (in //manman/src/host)

| Target | Type | Description |
|--------|------|-------------|
| `//manman/src/host:experience_api_spec` | `genrule` | Generate experience API spec |
| `//manman/src/host:status_api_spec` | `genrule` | Generate status API spec |
| `//manman/src/host:worker_dal_api_spec` | `genrule` | Generate worker DAL API spec |
| `//manman/src/host:all_api_specs` | `filegroup` | Generate all API specs |
| `//clients:generate_specs` | `alias` | Convenience alias to all_api_specs |

### Example: Use Generated Specs in Another Rule

```starlark
# In another BUILD.bazel file
genrule(
    name = "validate_specs",
    srcs = ["//libs/python/openapi_gen:all_api_specs"],
    outs = ["validation_report.txt"],
    cmd = """
        echo "Validating OpenAPI specs..." > $@
        for spec in $(SRCS); do
            echo "Validated: $$spec" >> $@
        done
    """,
)
```

## 🔄 Migration from Old Location

The OpenAPI generation tool was previously located at `manman/src/host/openapi.py`.

### Old Usage (Deprecated)

```bash
# Old way (still works but deprecated)
python -m manman.src.host.openapi experience-api
```

### New Usage (Recommended)

```bash
# New way via Bazel
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api

# Or generate as build artifacts
bazel build //libs/python/openapi_gen:all_api_specs
```

### Why Move?

1. **Better Separation** - OpenAPI generation is a build tool, not part of the host service
2. **Bazel Integration** - Can be used as a proper build target
3. **Reusability** - Can be imported as a library by other tools (e.g., client generator)
4. **Visibility** - Available to the entire monorepo, not just manman

## 🧪 Testing

Test the generation:

```bash
# Generate all specs
bazel build //libs/python/openapi_gen:all_api_specs

# Verify they exist
ls bazel-bin/libs/python/openapi_gen/*.json

# Validate JSON format
cat bazel-bin/libs/python/openapi_gen/experience-api.json | jq .
```

## 📚 Related Documentation

- [Client Generation Guide](../../../clients/README.md) - Using generated specs to create clients
- [API Implementation Guide](../../../manman/design/api-gen-implementation.md) - Complete client generation strategy
- [ManMan Config](../../../manman/src/config.py) - API configuration constants

## 🔗 Dependencies

- `fastapi` - FastAPI framework (for OpenAPI generation)
- `typer` - CLI framework
- `//manman/src:manman_core` - ManMan configuration and logging
- `//manman/src/host:manman_host` - FastAPI applications

## 💡 Tips

### Generate Specs During Build

Add to your CI/CD pipeline:

```yaml
# .github/workflows/ci.yml
- name: Generate OpenAPI Specs
  run: bazel build //libs/python/openapi_gen:all_api_specs

- name: Upload Specs
  uses: actions/upload-artifact@v3
  with:
    name: openapi-specs
    path: bazel-bin/libs/python/openapi_gen/*.json
```

### Watch for API Changes

Use `ibazel` to regenerate on changes:

```bash
ibazel build //libs/python/openapi_gen:all_api_specs
```

### Validate Specs

Use OpenAPI validator:

```bash
# Generate spec
bazel build //libs/python/openapi_gen:experience_api_spec

# Validate it
openapi-generator-cli validate \
  -i bazel-bin/libs/python/openapi_gen/experience-api.json
```

## 🐛 Troubleshooting

### "Module not found" errors

Make sure you're running from the project root:

```bash
cd /path/to/everything
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api
```

### "API name not found"

Valid API names are: `experience-api`, `status-api`, `worker-dal-api`

```bash
# Check available options
bazel run //libs/python/openapi_gen:openapi_gen -- --help
```

### Spec not updating

Clean and rebuild:

```bash
bazel clean
bazel build //libs/python/openapi_gen:all_api_specs
```
