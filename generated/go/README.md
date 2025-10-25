# OpenAPI Go Client Generation# OpenAPI Go Client Generation



This directory contains generated Go clients for OpenAPI specifications.This directory contains generated Go clients for OpenAPI specifications.



## How It Works## How It Works



**Pure Bazel approach**: Clients are generated on-demand during builds using custom Bazel rules. No manual sync steps required. Generated files are never committed to the repository.**Simple approach**: Generate clients via Bazel, sync to workspace, use normal `go_library` with `glob()`.



### Architecture### Why This Approach?



The OpenAPI client generation uses a two-step process:The fundamental challenge: Bazel requires declaring all outputs at analysis time, but OpenAPI generator creates different files based on each API's schema (which we only know at execution time).



1. **Generation** - `go_openapi_sources` rule runs openapi-generator to create a tar**Solutions considered**:

2. **Extraction** - Individual `.go` files are extracted into a tree artifact directory1. ❌ Repository rules - Complex, doesn't work well with workspace files

3. **Compilation** - `go_library` compiles from the extracted files2. ❌ Dynamic file listing - Can't use `glob()` on generated files in macros

3. ❌ Hardcoded file lists - Breaks when API schemas change

This happens automatically when you build targets that depend on the clients.4. ✅ **Sync to workspace** - Simple, works everywhere, IDE-friendly



### Why This Approach?## Usage



The fundamental challenge: Bazel requires declaring all outputs at analysis time, but OpenAPI generator creates different files based on each API's schema (which we only know at execution time).### 1. Regenerate Clients (after API spec changes)



**Solution**: Use Bazel tree artifacts (directory outputs) which can contain any files generated at execution time. The custom `go_openapi_sources` rule extracts individual `.go` files from the generator's output and returns them as a directory that `go_library` can consume.```bash

# Regenerate all Go clients

## Usage./tools/scripts/sync_go_clients.sh

```

### 1. Use in Your Code

This script:

```go1. Builds OpenAPI generator tars via Bazel

import "github.com/whale-net/everything/generated/go/manman/experience_api"2. Extracts generated `.go` files to `generated/go/{namespace}/{app}/`

3. Files are then available for normal Bazel builds and IDE autocomplete

// Use the client

client := experience_api.NewAPIClient(experience_api.NewConfiguration())### 2. Use in Your Code

```

```go

### 2. Add Dependency in BUILD.bazelimport "github.com/whale-net/everything/generated/go/manman/experience_api"



```python// Use the client

go_binary(client := experience_api.NewAPIClient(experience_api.NewConfiguration())

    name = "my_app",```

    deps = [

        "//generated/go/manman:experience_api",### 3. Add Dependency in BUILD.bazel

    ],

)```python

```go_binary(

    name = "my_app",

### 3. Build    deps = [

        "//generated/go/manman:experience_api",

```bash    ],

# Build your app - clients are generated automatically)

bazel build //my_app```



# Or build clients directly to verify generation## Available Clients

bazel build //generated/go/manman:experience_api

```- **`//generated/go/manman:experience_api`** - ManMan Experience API

- **`//generated/go/demo:hello_fastapi`** - Demo Hello FastAPI

## Available Clients

## Adding New Clients

- **`//generated/go/manman:experience_api`** - ManMan Experience API

- **`//generated/go/demo:hello_fastapi`** - Demo Hello FastAPI1. **Use the `openapi_go_client` macro** in `generated/go/{namespace}/BUILD.bazel`:



## Adding New Clients```python

load("//tools/openapi:openapi_go_client.bzl", "openapi_go_client")

1. **Use the `openapi_go_client` macro** in `generated/go/{namespace}/BUILD.bazel`:

openapi_go_client(

```python    name = "my_api",

load("//tools/openapi:openapi_go_client.bzl", "openapi_go_client")    spec = "//path/to:api_spec",

    namespace = "my_namespace",

openapi_go_client(    app = "my-api",

    name = "my_api",    importpath = "github.com/whale-net/everything/generated/go/my_namespace/my_api",

    spec = "//path/to:api_spec",)

    namespace = "my_namespace",```

    app = "my-api",

    importpath = "github.com/whale-net/everything/generated/go/my_namespace/my_api",The macro automatically creates:

)- `{name}_tar` - Genrule for tar generation

```- `{name}` - go_library with workspace-synced files



The macro automatically creates:2. **Add to sync script** (`tools/scripts/sync_go_clients.sh`):

- `{name}_srcs` - Custom rule that generates and extracts `.go` files

- `{name}` - `go_library` that compiles the generated code```bash

sync_client \

2. **Build to verify**:    "My API" \

    "//generated/go/my_namespace:my_api_tar" \

```bash    "generated/go/my_namespace/my_api" \

bazel build //generated/go/my_namespace:my_api    "bazel-bin/generated/go/my_namespace/my-api.tar"

``````



That's it! No sync scripts, no manual steps.3. **Run sync**:



## CI/CD Integration```bash

./tools/scripts/sync_go_clients.sh

No special integration needed! Generated files are **never committed** to git (see `.gitignore`). The build system handles everything:```



```yaml## CI/CD Integration

- name: Build

  run: bazel build //...The generated files are **excluded from git** (see `.gitignore`). In CI:

```

```yaml

## Local Development- name: Generate Go clients

  run: ./tools/scripts/sync_go_clients.sh

Generated files exist only in `bazel-bin/` during builds:

- ✅ Bazel handles all caching and incremental builds- name: Build

- ✅ Files regenerate automatically when specs change  run: bazel build //...

- ✅ No risk of committed files becoming out of sync```

- ✅ Clean workspace with only source files

## Local Development

For IDE support, you can load the generated files from `bazel-bin/` or use Bazel IDE plugins that understand generated sources.

Generated files are synced locally for:

## How The Generation Works- ✅ IDE autocomplete and type checking

- ✅ Fast local builds (no regeneration unless spec changes)

The `go_openapi_sources` rule in `tools/openapi/go_client.bzl`:- ✅ Normal Bazel dependency resolution



1. Runs `openapi-generator-cli` via wrapper script to create a tar## Troubleshooting

2. Extracts the tar to get individual `.go` files

3. Copies them into a tree artifact directory### "No such package" errors

4. Returns the directory for `go_library` to consume

Run the sync script:

The `openapi_go_client` macro in `tools/openapi/openapi_go_client.bzl`:```bash

./tools/scripts/sync_go_clients.sh

1. Invokes `go_openapi_sources` to generate files```

2. Creates a `go_library` that depends on the generated sources

3. Sets up proper import paths and visibility### Stale generated code



## TroubleshootingAfter modifying an OpenAPI spec, regenerate:

```bash

### "No such package" errors./tools/scripts/sync_go_clients.sh

```

Make sure you've defined the client using `openapi_go_client` in a BUILD.bazel file.

### Build failures after sync

### Build failures after spec changes

Check that the `go_library` in BUILD.bazel uses `glob()` to pick up all files:

Clean and rebuild:```python

```bashsrcs = glob(

bazel clean    ["my_api/*.go"],

bazel build //generated/go/...    exclude = ["my_api/*_test.go"],

```)

```

### Inspecting generated code

## How Python Clients Work Differently

Generated files are in `bazel-bin/`:

```bashPython clients use a **Bazel rule with directory outputs** - they declare a directory, not individual files. This works because Python's import system just needs a directory with `__init__.py`.

ls -la bazel-bin/generated/go/{namespace}/{app}_srcs/{app}/

```Go requires explicit source file lists for compilation, so we can't use the same approach. The sync-to-workspace pattern is the pragmatic solution.


## How Python Clients Work Differently

Python clients use a similar approach but with PyInfo providers that extract tars at runtime. Both languages now use pure Bazel solutions without manual sync steps.
