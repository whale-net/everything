# OpenAPI Client Generation

This directory contains automatically generated Python clients for all FastAPI services in the monorepo.

## Overview

Clients are generated from OpenAPI specifications and organized in the `external/{namespace}/{app}/` structure:

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
        "//clients:manman_status_api",  # Add the client dependency
        # ... other deps
    ],
)
```

## Building Clients

### Build All Clients

```bash
bazel build //clients:all_clients --java_runtime_version=remotejdk_17
```

### Build Individual Client

```bash
bazel build //clients:manman_experience_api --java_runtime_version=remotejdk_17
bazel build //clients:manman_status_api --java_runtime_version=remotejdk_17
bazel build //clients:demo_hello_fastapi --java_runtime_version=remotejdk_17
```

### Set Java Runtime as Default

Add to your `.bazelrc`:
```
build --java_runtime_version=remotejdk_17
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

1. **Add client target** in `//clients/BUILD.bazel`:

```starlark
openapi_client(
    name = "my_new_api",
    spec = "//path/to/your:app_openapi_spec",
    namespace = "myapp",      # Group related APIs
    app = "my_new_api",       # Specific API name
)
```

2. **Build the client**:

```bash
bazel build //clients:my_new_api --java_runtime_version=remotejdk_17
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
- Clients are generated in `//clients/` as separate artifacts from service implementations

See [CLIENT_GENERATION.md](../tools/CLIENT_GENERATION.md) for detailed explanation.

## Implementation Details

- **Rule**: `//tools:openapi_client.bzl`
- **Generator**: OpenAPI Generator 7.10.0 (Python client)
- **Library**: urllib3-based HTTP client
- **Dependencies**: Minimal (pydantic only at build time)

## Troubleshooting

### "No module named 'external'"

Make sure `bazel-bin/clients` is in your Python path:

```python
import sys
sys.path.insert(0, 'bazel-bin/clients')
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
bazel build //clients:your_client --java_runtime_version=remotejdk_17
```

### Import errors in generated code

The build process automatically fixes imports from package-relative to absolute `external.{namespace}.{app}` paths. If you see issues, check that the build completed successfully.

## References

- [CLIENT_GENERATION.md](../tools/CLIENT_GENERATION.md) - Full implementation plan
- [OpenAPI Generator Python Docs](https://openapi-generator.tech/docs/generators/python)
- [rules_java Documentation](https://github.com/bazelbuild/rules_java)
