# OpenAPI Client Generation

Automatically generate Python clients from FastAPI applications in the monorepo.

## Quick Start

### 1. Enable OpenAPI Generation for Your App

Add the `fastapi_app` parameter to your `release_app()` call:

```starlark
# In your app's BUILD.bazel
release_app(
    name = "my-api",
    language = "python",
    domain = "myservice",
    description = "My FastAPI application",
    app_type = "external-api",
    fastapi_app = "mypackage.mymodule.main:app",  # module_path:variable_name
)
```

This automatically creates a `{name}_openapi_spec` target that generates the OpenAPI JSON specification.

### 2. Build OpenAPI Spec

```bash
# Build specific app's spec
bazel build //path/to/app:my-api_openapi_spec

# Build all OpenAPI specs
bazel build $(bazel query 'attr(tags, openapi, //...)' 2>/dev/null)

# View generated spec
cat bazel-bin/path/to/app/my-api_openapi_spec.json | jq .
```

### 3. Generate Python Client

```bash
# Generate client for specific API
python tools/generate_clients.py --api my-api

# Or manually using OpenAPI Generator
openapi-generator-cli generate \
  -i bazel-bin/path/to/app/my-api_openapi_spec.json \
  -g python \
  -o clients/my-api-client \
  --additional-properties=packageName=my_api_client
```

## How It Works

1. **Automatic Target Creation**: When you add `fastapi_app` to `release_app()`, it automatically creates an OpenAPI spec generation target
2. **Zero Configuration**: No manual mapping or configuration files needed
3. **Dependency Management**: The Bazel rule ensures all app dependencies are available during spec generation
4. **Convention over Configuration**: Just specify the module path and variable name

## Examples

### Simple FastAPI App

```python
# demo/hello_fastapi/main.py
from fastapi import FastAPI

app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "hello world"}
```

```starlark
# demo/hello_fastapi/BUILD.bazel
release_app(
    name = "hello-fastapi",
    language = "python",
    domain = "demo",
    fastapi_app = "demo.hello_fastapi.main:app",  # Automagic!
)
```

### App with Custom Variable Name

```python
# myapp/api.py
from fastapi import FastAPI

my_application = FastAPI()

@my_application.get("/status")
def status():
    return {"status": "ok"}
```

```starlark
# myapp/BUILD.bazel
release_app(
    name = "my-api",
    language = "python",
    domain = "services",
    fastapi_app = "myapp.api:my_application",  # Custom variable name
)
```

## Discovery

Find all apps with OpenAPI specs:

```bash
# List all OpenAPI spec targets
bazel query 'attr(tags, openapi, //...)'

# Build them all
bazel build $(bazel query 'attr(tags, openapi, //...)' 2>/dev/null)
```

## Advanced: Client Installation

```bash
# Install generated client locally
pip install -e ./clients/my-api-client

# Use in your code
from my_api_client import ApiClient, Configuration
from my_api_client.api import DefaultApi

config = Configuration(host="http://localhost:8000")
with ApiClient(configuration=config) as client:
    api = DefaultApi(client)
    response = api.some_endpoint()
```

## Examples in This Repo

- `//demo/hello_fastapi:hello-fastapi_openapi_spec` - Simple FastAPI demo
- `//manman/src/host:experience_api_spec` - ManMan experience API (custom generation)
- `//manman/src/host:status_api_spec` - ManMan status API (custom generation)
- `//manman/src/host:worker_dal_api_spec` - ManMan worker DAL API (custom generation)
