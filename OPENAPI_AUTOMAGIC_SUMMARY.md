# OpenAPI Automagic Generation

## Summary

Extended the `release_app()` macro to automatically generate OpenAPI specifications for FastAPI applications with zero manual configuration.

## What Changed

### 1. New Bazel Rule: `openapi.bzl`

Created `//tools:openapi.bzl` with an `openapi_spec()` rule that:
- Generates a Python script to import and introspect FastAPI apps
- Creates a `py_binary` with proper dependencies
- Outputs OpenAPI JSON specification

### 2. Extended `release_app()` Macro

Added `fastapi_app` parameter to `release_app()` in `//tools:release.bzl`:

```starlark
release_app(
    name = "my-api",
    language = "python",
    domain = "demo",
    fastapi_app = "module.path:variable_name",  # NEW!
)
```

When specified, automatically creates `{name}_openapi_spec` target.

### 3. Generic Tools (Unused Currently)

Created generic tools in `//libs/python/openapi_gen`:
- `openapi_gen_generic.py` - Importlib-based generator (not used yet)
- `openapi_gen.py` - ManMan-specific generator (still used for ManMan APIs)

## Usage

### For New Apps

```starlark
# demo/hello_fastapi/BUILD.bazel
release_app(
    name = "hello-fastapi",
    language = "python",
    domain = "demo",
    fastapi_app = "demo.hello_fastapi.main:app",
)
```

Automatically creates:
- `//demo/hello_fastapi:hello-fastapi_openapi_spec`

### Building Specs

```bash
# Build specific spec
bazel build //demo/hello_fastapi:hello-fastapi_openapi_spec

# Build all specs
bazel build $(bazel query 'attr(tags, openapi, //...)')

# View spec
cat bazel-bin/demo/hello_fastapi/hello-fastapi_openapi_spec.json | jq .
```

### Discovery

```bash
# Find all apps with OpenAPI specs
bazel query 'attr(tags, openapi, //...)'
```

## Examples

### Simple Demo App

```python
# demo/hello_fastapi/main.py
from fastapi import FastAPI

app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "hello world"}
```

Spec target: `//demo/hello_fastapi:hello-fastapi_openapi_spec`

### ManMan APIs (Manual Configuration)

ManMan still uses manual spec generation in `//manman/src/host/BUILD.bazel` because it uses a custom tool that understands ManMan's API structure.

## Benefits

1. **Zero Configuration**: Just add one parameter to `release_app()`
2. **Automatic Discovery**: Query for all specs with Bazel tags
3. **Dependency Management**: Bazel ensures all dependencies are available
4. **Convention over Configuration**: Works with any FastAPI app
5. **No Manual Mappings**: No need to maintain lists of APIs in tools

## Technical Details

### How It Works

1. `release_app()` with `fastapi_app` parameter calls `openapi_spec()` rule
2. Rule generates a Python script that:
   ```python
   import importlib
   module = importlib.import_module("demo.hello_fastapi.main")
   app = getattr(module, "app")
   spec = app.openapi()
   ```
3. Script is wrapped in `py_binary` with app as dependency
4. `genrule` executes the binary and captures output to JSON

### Files Created

- `//tools/openapi.bzl` - Bazel rule for OpenAPI generation
- `//libs/python/openapi_gen/openapi_gen_generic.py` - Generic CLI tool (not used yet)
- Updated `//tools/release.bzl` - Added `fastapi_app` parameter
- Updated `//clients/README.md` - Simplified documentation
- Updated `//demo/hello_fastapi/BUILD.bazel` - Example usage

### Backward Compatibility

- ManMan APIs continue to work with manual spec generation
- No breaking changes to existing code
- Can gradually migrate other APIs to use automagic generation

## Next Steps

1. **Migrate ManMan APIs**: Convert ManMan APIs to use automagic generation (optional)
2. **Client Generation**: Update `tools/generate_clients.py` to discover specs automatically
3. **CI Integration**: Add workflow to build all specs and validate them
4. **Documentation**: Add to developer docs

## Testing

All specs build successfully:
```bash
$ bazel build //demo/hello_fastapi:hello-fastapi_openapi_spec //manman/src/host:all_api_specs
INFO: Build completed successfully
```

Specs are properly formatted:
```bash
$ cat bazel-bin/demo/hello_fastapi/hello-fastapi_openapi_spec.json | jq .info
{
  "title": "FastAPI",
  "version": "0.1.0"
}
```
