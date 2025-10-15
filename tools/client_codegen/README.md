# OpenAPI Client Generation


This directory contains examples and tests for OpenAPI client generation.

**Production ManMan API clients** are defined in `//manman/src/host:BUILD.bazel` alongside their OpenAPI spec definitions for better organization and colocation.

## Overview

OpenAPI clients are Python libraries automatically generated from OpenAPI specifications. They provide type-safe, well-documented interfaces for consuming APIs.

### Client Organization

- **ManMan API clients**: Defined in `//manman/src/host:BUILD.bazel`
  - `//manman/src/host:experience_api_client`
  - `//manman/src/host:status_api_client`
  - `//manman/src/host:worker_dal_api_client`
  - `//manman/src/host:all_api_clients` (convenience target)

- **Demo/Example clients**: Defined in this directory
  - `//tools/client_codegen:demo_hello_fastapi`

### Import Pattern

Generated clients use the `external/{namespace}/{app}/` structure:

```
external/
├── demo/
│   └── hello_fastapi/      # Demo API client
└── manman/
    ├── experience_api/     # ManMan Experience API client
    ├── status_api/         # ManMan Status API client
    └── worker_dal_api/     # ManMan Worker DAL API client
```

## Usage

### Import Pattern

```python
# Import from external.{namespace}.{app}
from external.manman.experience_api import ApiClient, Configuration
from external.manman.experience_api.api.default_api import DefaultApi
from external.manman.status_api.models.external_status_info import ExternalStatusInfo
```

### Basic Usage Example

```python
from external.manman.status_api import ApiClient, Configuration
from external.manman.status_api.api.default_api import DefaultApi

# Configure the client
config = Configuration(host="http://status-api:8000")

# Make API calls
with ApiClient(config) as client:
    api = DefaultApi(client)
    status = api.get_status()
    print(status)
```

### Using in Bazel Targets

Add the client as a dependency in your `BUILD.bazel`:

```starlark
py_binary(
    name = "my_service",
    srcs = ["main.py"],
    deps = [
        "//manman/src/host:status_api_client",  # Use ManMan API clients
        # ... other deps
    ],
)
```

## Building Clients

### Build ManMan API Clients

```bash
# Build individual clients
bazel build //manman/src/host:experience_api_client
bazel build //manman/src/host:status_api_client
bazel build //manman/src/host:worker_dal_api_client

# Build all ManMan clients
bazel build //manman/src/host:all_api_clients
```

### Build Demo Client

```bash
bazel build //tools/client_codegen:demo_hello_fastapi
```

## Generated Client Structure

Each client contains:

- `api/` - API endpoint classes (e.g., `DefaultApi`)
- `models/` - Pydantic model classes for request/response
- `api_client.py` - Low-level HTTP client
- `configuration.py` - Client configuration (host, auth, etc.)
- `exceptions.py` - API exception classes
- `rest.py` - REST client utilities

## Adding New API Clients

When you create a new FastAPI app with `release_app()`, the OpenAPI spec is automatically generated. To add a client:

1. **For ManMan APIs**: Add client target in `//manman/src/host/BUILD.bazel`:

```starlark
openapi_client(
    name = "my_new_api_client",
    spec = ":my_new_api_spec",
    namespace = "manman",
    app = "my_new_api",
    visibility = ["//visibility:public"],
)
```

2. **For demo/other APIs**: Add client target in `//tools/client_codegen/BUILD.bazel`:

```starlark
openapi_client(
    name = "my_demo_api",
    spec = "//path/to/your:app_openapi_spec",
    namespace = "demo",
    app = "my_demo_api",
)
```

3. **Build the client**:

```bash
# ManMan APIs
bazel build //manman/src/host:my_new_api_client

# Demo APIs
bazel build //tools/client_codegen:my_demo_api
```

3. **Import and use**:

```python
from external.myapp.my_new_api import ApiClient, Configuration
```

## Configuration Options

### Authentication

```python
from external.manman.experience_api import Configuration

config = Configuration(
    host="http://api-host:8000",
    access_token="your-jwt-token",  # Bearer token
)
```

### Timeouts and Retries

```python
config = Configuration(
    host="http://api-host:8000",
)
config.timeout = 30  # seconds
```

### Async Support

The generated clients support both sync and async operations (depending on OpenAPI Generator version and configuration).

## IDE Setup

For autocomplete and type hints in your IDE:

### VS Code

Add to `.vscode/settings.json`:

```json
{
  "python.analysis.extraPaths": [
    "${workspaceFolder}/bazel-bin/clients"
  ]
}
```

### PyCharm

1. Go to Settings → Project → Project Structure
2. Add Content Root: `<project>/bazel-bin/clients`
3. Mark as Sources

### Symlink Approach (Any IDE)

Create a symlink in your repository root:

```bash
ln -s bazel-bin/clients/external external
```

Then your IDE will find the `external/` imports automatically.

## Circular Dependencies

The client generation approach handles circular dependencies correctly:

- Service A can depend on Service B's **client** (not B's implementation)
- Service B can depend on Service A's **client** (not A's implementation)
- Clients are generated separately from service implementations

See archived [CLIENT_GENERATION.md](../../docs/archive/CLIENT_GENERATION.md) for detailed explanation.

## Implementation Details

- **Rule**: `//tools:openapi_client.bzl`
- **Generator**: OpenAPI Generator 7.10.0 (Python client)
- **Library**: urllib3-based HTTP client
- **Dependencies**: Minimal (pydantic only at build time)

## Troubleshooting

### "No module named 'external'"

Make sure the generated client's bazel-bin directory is in your Python path:

```python
import sys
# For ManMan clients
sys.path.insert(0, 'bazel-bin/manman/src/host')
# For demo clients
sys.path.insert(0, 'bazel-bin/tools/client_codegen')
```

Or add it to `PYTHONPATH`:

```bash
export PYTHONPATH="${PWD}/bazel-bin/clients:${PYTHONPATH}"
```

### "No module named 'pydantic'"

The generated clients require pydantic at runtime. It should already be in your `uv.lock`. If not, add it:

```bash
uv add pydantic
```

### Clients not updating after API changes

Rebuild the OpenAPI spec and client:

```bash
bazel clean
# ManMan clients
bazel build //manman/src/host:your_client
# Demo clients
bazel build //tools/client_codegen:your_client
```

### Import errors in generated code

The build process automatically fixes imports from package-relative to absolute `external.{namespace}.{app}` paths. If you see issues, check that the build completed successfully.

## References

- [CLIENT_GENERATION.md](../docs/archive/CLIENT_GENERATION.md) - Archived implementation plan
- [OpenAPI Generator Python Docs](https://openapi-generator.tech/docs/generators/python)
- [rules_java Documentation](https://github.com/bazelbuild/rules_java)
