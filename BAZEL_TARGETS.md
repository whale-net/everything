# Bazel Targets for OpenAPI & Client Generation

Quick reference for all Bazel targets related to OpenAPI spec generation and client generation.

## 📐 Architecture

- **Tool Library:** `//libs/python/openapi_gen` - Reusable OpenAPI generation tool
- **API Specs:** `//manman/src/host` - OpenAPI spec targets (defined alongside the APIs)
- **Client Generation:** `//clients` - Client generation utilities

## 🎯 OpenAPI Spec Generation

### Generate All Specs

```bash
# Generate all API specs at once (recommended)
bazel build //manman/src/host:all_api_specs

# View generated specs
ls bazel-bin/manman/src/host/*.json
cat bazel-bin/manman/src/host/experience-api.json

# Or use convenience alias
bazel build //clients:generate_specs
```

### Generate Individual Specs

```bash
# Experience API
bazel build //manman/src/host:experience_api_spec
cat bazel-bin/manman/src/host/experience-api.json

# Status API  
bazel build //manman/src/host:status_api_spec
cat bazel-bin/manman/src/host/status-api.json

# Worker DAL API
bazel build //manman/src/host:worker_dal_api_spec
cat bazel-bin/manman/src/host/worker-dal-api.json
```

### Run Generation CLI

```bash
# Run the OpenAPI generator CLI via Bazel
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api

# With custom output directory
bazel run //libs/python/openapi_gen:openapi_gen -- status-api --output-dir ./my-specs

# Help
bazel run //libs/python/openapi_gen:openapi_gen -- --help
```

## 🔧 Client Generation

### Generate OpenAPI Specs (for client generation)

```bash
# Convenience alias to generate all specs
bazel build //clients:generate_specs

# This is equivalent to:
bazel build //manman/src/host:all_api_specs
```

### Generate Clients (Python Script)

```bash
# Run the client generation script
bazel run //clients:generate_clients

# Note: This is a wrapper that calls:
# python tools/generate_clients.py
```

## 📚 Available Targets

### OpenAPI Generation Tool (//libs/python/openapi_gen)

| Target | Type | Output | Description |
|--------|------|--------|-------------|
| `//libs/python/openapi_gen:openapi_gen_lib` | `py_library` | - | Library for importing in Python code |
| `//libs/python/openapi_gen:openapi_gen` | `py_binary` | - | CLI tool (run with `bazel run`) |

### API Spec Targets (//manman/src/host)

| Target | Type | Output | Description |
|--------|------|--------|-------------|
| `//manman/src/host:experience_api_spec` | `genrule` | `experience-api.json` | Generate experience API spec |
| `//manman/src/host:status_api_spec` | `genrule` | `status-api.json` | Generate status API spec |
| `//manman/src/host:worker_dal_api_spec` | `genrule` | `worker-dal-api.json` | Generate worker DAL API spec |
| `//manman/src/host:all_api_specs` | `filegroup` | All specs | Generate all API specs |

### Client Generation (//clients)

| Target | Type | Output | Description |
|--------|------|--------|-------------|
| `//clients:generate_specs` | `alias` | All specs | Alias to `//manman/src/host:all_api_specs` |
| `//clients:generate_clients` | `py_binary` | - | Client generation script |

## 🔄 Common Workflows

### Workflow 1: Generate Specs Only

```bash
# Build all specs as artifacts
bazel build //manman/src/host:all_api_specs

# Copy to workspace for external tools
cp bazel-bin/manman/src/host/*.json openapi-specs/
```

### Workflow 2: Generate Specs + Clients

```bash
# Step 1: Generate specs
bazel build //manman/src/host:all_api_specs

# Step 2: Generate clients (this also generates specs internally)
python tools/generate_clients.py --strategy=shared --build-wheel

# Step 3: Install client
pip install clients/worker-dal-api-client/dist/*.whl
```

### Workflow 3: Watch for Changes

```bash
# Auto-regenerate specs on API changes
ibazel build //manman/src/host:all_api_specs
```

### Workflow 4: Validate Specs

```bash
# Generate spec
bazel build //manman/src/host:experience_api_spec

# Validate with OpenAPI Generator
openapi-generator-cli validate \
  -i bazel-bin/manman/src/host/experience-api.json
```

## 🎨 Using in Other Rules

### Example: Validation Rule

```starlark
# In your BUILD.bazel file
genrule(
    name = "validate_openapi_specs",
    srcs = ["//manman/src/host:all_api_specs"],
    outs = ["validation.txt"],
    cmd = """
        echo "Validating specs..." > $@
        for spec in $(SRCS); do
            echo "✓ $$spec" >> $@
        done
    """,
)
```

### Example: Copy Specs to Directory

```starlark
genrule(
    name = "copy_specs_to_docs",
    srcs = ["//manman/src/host:all_api_specs"],
    outs = ["docs/specs/copied.txt"],
    cmd = """
        mkdir -p docs/specs
        cp $(SRCS) docs/specs/
        echo "Specs copied" > $@
    """,
)
```

## 🐛 Troubleshooting

### Specs not updating?

```bash
# Clean and rebuild
bazel clean
bazel build //manman/src/host:all_api_specs
```

### Can't find generated spec?

```bash
# Check bazel-bin directory
ls -la bazel-bin/manman/src/host/

# Or use bazel to show output location
bazel info output_path
```

### "Target not found" error?

```bash
# Make sure you're in the project root
cd /path/to/everything

# List available targets
bazel query //manman/src/host:*api_spec
bazel query //libs/python/openapi_gen:*
```

## 📖 Related Documentation

- [OpenAPI Gen README](../libs/python/openapi_gen/README.md) - Detailed library documentation
- [Client Generation Guide](../clients/README.md) - Using specs to generate clients
- [Implementation Guide](../manman/design/api-gen-implementation.md) - Complete strategy guide

## 💡 Tips

### Tip 1: Use in CI/CD

```yaml
# .github/workflows/ci.yml
- name: Generate OpenAPI Specs
  run: bazel build //manman/src/host:all_api_specs

- name: Validate Specs
  run: |
    for spec in bazel-bin/manman/src/host/*.json; do
      openapi-generator-cli validate -i $spec
    done
```

### Tip 2: Pre-commit Hook

```bash
# .git/hooks/pre-commit
#!/bin/bash
bazel build //manman/src/host:all_api_specs
```

### Tip 3: Development Workflow

```bash
# Terminal 1: Watch for changes
ibazel build //manman/src/host:all_api_specs

# Terminal 2: Make API changes
vim manman/src/host/api/experience/api.py

# Specs automatically regenerate in Terminal 1
```

## 🚀 Quick Command Reference

```bash
# Build all specs
bazel build //manman/src/host:all_api_specs

# Run CLI tool
bazel run //libs/python/openapi_gen:openapi_gen -- experience-api

# Generate clients
python tools/generate_clients.py --strategy=shared
```
