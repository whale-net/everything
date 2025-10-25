# OpenAPI Go Client Generation

This directory contains generated Go clients for OpenAPI specifications.

## How It Works

**Simple approach**: Generate clients via Bazel, sync to workspace, use normal `go_library` with `glob()`.

### Why This Approach?

The fundamental challenge: Bazel requires declaring all outputs at analysis time, but OpenAPI generator creates different files based on each API's schema (which we only know at execution time).

**Solutions considered**:
1. ❌ Repository rules - Complex, doesn't work well with workspace files
2. ❌ Dynamic file listing - Can't use `glob()` on generated files in macros
3. ❌ Hardcoded file lists - Breaks when API schemas change
4. ✅ **Sync to workspace** - Simple, works everywhere, IDE-friendly

## Usage

### 1. Regenerate Clients (after API spec changes)

```bash
# Regenerate all Go clients
./tools/scripts/sync_go_clients.sh
```

This script:
1. Builds OpenAPI generator tars via Bazel
2. Extracts generated `.go` files to `generated/go/{namespace}/{app}/`
3. Files are then available for normal Bazel builds and IDE autocomplete

### 2. Use in Your Code

```go
import "github.com/whale-net/everything/generated/go/manman/experience_api"

// Use the client
client := experience_api.NewAPIClient(experience_api.NewConfiguration())
```

### 3. Add Dependency in BUILD.bazel

```python
go_binary(
    name = "my_app",
    deps = [
        "//generated/go/manman:experience_api",
    ],
)
```

## Available Clients

- **`//generated/go/manman:experience_api`** - ManMan Experience API
- **`//generated/go/demo:hello_fastapi`** - Demo Hello FastAPI

## Adding New Clients

1. **Create genrule for tar generation** in `generated/go/{namespace}/BUILD.bazel`:

```python
genrule(
    name = "my_api_tar",
    srcs = ["//path/to:api_spec"],
    outs = ["my-api.tar"],
    tools = [
        "//tools/openapi:openapi_gen_go_wrapper",
        "@openapi_generator_cli//file",
    ],
    toolchains = ["@bazel_tools//tools/jdk:current_java_runtime"],
    cmd = """
        $(location //tools/openapi:openapi_gen_go_wrapper) \\
            auto \\
            $(JAVA) \\
            $(location @openapi_generator_cli//file) \\
            $(location //path/to:api_spec) \\
            $@ \\
            my_api \\
            github.com/whale-net/everything/generated/go/namespace/my_api
    """,
    tags = ["openapi", "go", "manual"],
)
```

2. **Create go_library**:

```python
go_library(
    name = "my_api",
    srcs = glob(
        ["my_api/*.go"],
        exclude = ["my_api/*_test.go"],
    ),
    importpath = "github.com/whale-net/everything/generated/go/namespace/my_api",
    visibility = ["//visibility:public"],
)
```

3. **Add to sync script** (`tools/scripts/sync_go_clients.sh`):

```bash
sync_client \
    "My API" \
    "//generated/go/namespace:my_api_tar" \
    "generated/go/namespace/my_api" \
    "bazel-bin/generated/go/namespace/my-api.tar"
```

4. **Run sync**:

```bash
./tools/scripts/sync_go_clients.sh
```

## CI/CD Integration

The generated files are **excluded from git** (see `.gitignore`). In CI:

```yaml
- name: Generate Go clients
  run: ./tools/scripts/sync_go_clients.sh

- name: Build
  run: bazel build //...
```

## Local Development

Generated files are synced locally for:
- ✅ IDE autocomplete and type checking
- ✅ Fast local builds (no regeneration unless spec changes)
- ✅ Normal Bazel dependency resolution

## Troubleshooting

### "No such package" errors

Run the sync script:
```bash
./tools/scripts/sync_go_clients.sh
```

### Stale generated code

After modifying an OpenAPI spec, regenerate:
```bash
./tools/scripts/sync_go_clients.sh
```

### Build failures after sync

Check that the `go_library` in BUILD.bazel uses `glob()` to pick up all files:
```python
srcs = glob(
    ["my_api/*.go"],
    exclude = ["my_api/*_test.go"],
)
```

## How Python Clients Work Differently

Python clients use a **Bazel rule with directory outputs** - they declare a directory, not individual files. This works because Python's import system just needs a directory with `__init__.py`.

Go requires explicit source file lists for compilation, so we can't use the same approach. The sync-to-workspace pattern is the pragmatic solution.
