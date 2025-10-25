# OpenAPI Go Client Generation

This directory now supports generating both Python and Go clients from OpenAPI specifications.

## Python Clients

Python clients are generated using the `openapi_client` rule:

```starlark
load("//tools/openapi:openapi_client_rule.bzl", "openapi_client")

openapi_client(
    name = "my_api",
    spec = "//path/to:openapi_spec",
    namespace = "demo",
    app = "my_api",
)
```

Import pattern: `from generated.py.{namespace}.{app} import ...`

## Go Clients

Go clients are generated using the `openapi_go_client` rule:

```starlark
load("//tools/openapi:openapi_go_client.bzl", "openapi_go_client")

openapi_go_client(
    name = "my_api_go",
    spec = "//path/to:openapi_spec",
    namespace = "demo",
    app = "my_api_go",
    importpath = "github.com/whale-net/everything/generated/go/demo/my_api_go",
)
```

Import pattern: `import "github.com/whale-net/everything/generated/go/{namespace}/{app}"`

### Using Go Clients

To use a generated Go client in your code:

```go
package main

import (
    "fmt"
    client "github.com/whale-net/everything/generated/go/demo/hello_fastapi_go"
)

func main() {
    cfg := client.NewConfiguration()
    cfg.Host = "localhost:8000"
    cfg.Scheme = "http"
    
    apiClient := client.NewAPIClient(cfg)
    // Use apiClient to make API calls...
}
```

In your BUILD.bazel:

```starlark
load("@rules_go//go:def.bzl", "go_binary")

go_binary(
    name = "my_app",
    srcs = ["main.go"],
    deps = [
        "//generated/go/demo:hello_fastapi_go",
    ],
)
```

## Examples

- Python client: `//generated/py/demo:hello_fastapi`
- Go client: `//generated/go/demo:hello_fastapi_go`
- Go usage example: `//demo/hello_go_client:hello_go_client`

## Building and Testing

```bash
# Build Python client
bazel build //generated/py/demo:hello_fastapi

# Build Go client
bazel build //generated/go/demo:hello_fastapi_go

# Run Go example
bazel run //demo/hello_go_client:hello_go_client
```

## Architecture

Both Python and Go clients use the same OpenAPI Generator CLI tool:

1. **Python**: Uses `openapi_gen_wrapper.sh` with `-g python` generator
2. **Go**: Uses `openapi_gen_go_wrapper.sh` with `-g go` generator

Generated files are packaged as tar archives and extracted into the appropriate package structure for each language.
