# OpenAPI Client Generation Implementation Plan

## Overview

This document outlines the implementation plan for automatic OpenAPI client generation using the `external/` directory pattern. Clients will be generated in a clean namespace structure: `from external.{namespace}.{app}` where the client library name itself makes it clear it's a client.

## Import Pattern

### Target Structure
```python
# Generated clients go in external/{namespace}/{app}/
from external.manman.experience_api import ExperienceApi
from external.manman.status_api import StatusApi, Worker
from external.demo.hello_fastapi import HelloFastapiApi

# Internal code stays clean
from manman.src.host.experience_api import ExperienceApiApp
from libs.python import utils
```

### Rationale
- **`external/`**: Clear signal that this is generated/external code, not internal application code
- **`{namespace}/`**: Groups related APIs (e.g., all ManMan APIs under `manman/`)
- **`{app}/`**: The specific API name (e.g., `experience_api`, `status_api`)
- No redundant `_client` suffix since the path already indicates it's a client library

## Implementation Steps

### 1. Create `openapi_client.bzl` Rule

**Location**: `//tools/openapi_client.bzl`

**Purpose**: Bazel rule that takes an OpenAPI spec and generates a Python client library in the `external/` directory structure.

**Interface**:
```starlark
openapi_client(
    name = "manman_experience_api_client",
    spec = "//manman/src/host:experience_api_spec",
    namespace = "manman",
    app = "experience_api",
    package_name = "manman-experience-api",  # For PyPI-style naming in setup.py
)
```

**Outputs**:
- Python client library at `external/{namespace}/{app}/`
- `py_library` target exposing the generated code
- Proper `__init__.py` files for clean imports

**Implementation Details**:
```starlark
def openapi_client(name, spec, namespace, app, package_name = None):
    """Generate OpenAPI client library in external/ directory.
    
    Args:
        name: Target name for the generated py_library
        spec: Label pointing to OpenAPI spec JSON file
        namespace: Namespace for grouping (e.g., "manman", "demo")
        app: Application name (e.g., "experience_api", "hello_fastapi")
        package_name: Optional package name for setup.py (defaults to {namespace}-{app})
    """
    
    if not package_name:
        package_name = "{}-{}".format(namespace, app)
    
    output_dir = "external/{}/{}".format(namespace, app)
    
    native.genrule(
        name = name + "_gen",
        srcs = [spec],
        outs = [
            output_dir + "/__init__.py",
            output_dir + "/api/__init__.py",
            output_dir + "/api/default_api.py",
            output_dir + "/models/__init__.py",
            # ... other generated files
        ],
        cmd = """
            # Run OpenAPI Generator
            $(location @openapi_generator_cli//:bin) generate \
                -i $(location {spec}) \
                -g python \
                -o $(GENDIR)/{output_dir} \
                --package-name {package_name} \
                --additional-properties=generateSourceCodeOnly=true
            
            # Clean up unnecessary files
            rm -rf $(GENDIR)/{output_dir}/test
            rm -rf $(GENDIR)/{output_dir}/docs
        """.format(
            spec = spec,
            output_dir = output_dir,
            package_name = package_name,
        ),
        tools = ["@openapi_generator_cli//:bin"],
    )
    
    native.py_library(
        name = name,
        srcs = native.glob([output_dir + "/**/*.py"]),
        imports = ["external"],  # Make external/ a Python import root
        deps = [
            "@pip//pydantic",
            "@pip//typing_extensions",
            "@pip//urllib3",
        ],
    )
```

### 2. Add OpenAPI Generator Dependency

**Location**: `MODULE.bazel`

Add OpenAPI Generator CLI as a Bazel dependency:

```starlark
# Option 1: Use http_file to download OpenAPI Generator JAR
http_file(
    name = "openapi_generator_cli",
    urls = ["https://repo1.maven.org/maven2/org/openapitools/openapi-generator-cli/7.10.0/openapi-generator-cli-7.10.0.jar"],
    sha256 = "...",  # Add actual SHA256
    downloaded_file_path = "openapi-generator-cli.jar",
)

# Option 2: Use rules_jvm_external for Maven dependency
maven_install(
    artifacts = [
        "org.openapitools:openapi-generator-cli:7.10.0",
    ],
    repositories = [
        "https://repo1.maven.org/maven2",
    ],
)
```

### 3. Create Client Generation Targets

**Location**: `//clients/BUILD.bazel`

Define client generation targets for all APIs:

```starlark
load("//tools:openapi_client.bzl", "openapi_client")

# ManMan API Clients
openapi_client(
    name = "manman_experience_api",
    spec = "//manman/src/host:experience_api_spec",
    namespace = "manman",
    app = "experience_api",
)

openapi_client(
    name = "manman_status_api",
    spec = "//manman/src/host:status_api_spec",
    namespace = "manman",
    app = "status_api",
)

openapi_client(
    name = "manman_worker_dal_api",
    spec = "//manman/src/host:worker_dal_api_spec",
    namespace = "manman",
    app = "worker_dal_api",
)

# Demo API Clients
openapi_client(
    name = "demo_hello_fastapi",
    spec = "//demo/hello_fastapi:hello-fastapi_openapi_spec",
    namespace = "demo",
    app = "hello_fastapi",
)

# Convenience target to build all clients
filegroup(
    name = "all_clients",
    srcs = [
        ":manman_experience_api",
        ":manman_status_api",
        ":manman_worker_dal_api",
        ":demo_hello_fastapi",
    ],
    visibility = ["//visibility:public"],
)
```

### 4. Update Consuming Services

**Example**: ManMan worker consuming Status API

**Before** (manual API calls):
```python
# manman/src/worker/some_module.py
import httpx

async def get_worker_status(worker_id: str):
    async with httpx.AsyncClient() as client:
        response = await client.get(f"http://status-api/workers/{worker_id}")
        return response.json()
```

**After** (generated client):
```python
# manman/src/worker/some_module.py
from external.manman.status_api import DefaultApi, ApiClient, Configuration

async def get_worker_status(worker_id: str):
    config = Configuration(host="http://status-api")
    async with ApiClient(config) as client:
        api = DefaultApi(client)
        return await api.get_worker(worker_id)
```

**BUILD.bazel update**:
```starlark
py_binary(
    name = "worker",
    srcs = ["some_module.py"],
    deps = [
        "//clients:manman_status_api",  # Add client dependency
    ],
)
```

### 5. Configure IDE Support

**Location**: `.vscode/settings.json` (or similar for other IDEs)

Enable autocomplete for generated clients:

```json
{
  "python.analysis.extraPaths": [
    "${workspaceFolder}/bazel-bin/clients",
    "${workspaceFolder}/bazel-bin"
  ],
  "python.autoComplete.extraPaths": [
    "${workspaceFolder}/bazel-bin/clients",
    "${workspaceFolder}/bazel-bin"
  ]
}
```

**Alternative**: Create symlink for easier IDE integration:
```bash
# In repository root
ln -s bazel-bin/clients/external external
```

### 6. Extend `release_app` Macro (Optional)

**Location**: `//tools/release.bzl`

Optionally extend `release_app` to automatically create client generation targets:

```starlark
def release_app(
    name,
    binary_target,
    language,
    domain,
    fastapi_app = None,
    generate_client = False,  # New parameter
    client_namespace = None,  # New parameter
    **kwargs
):
    # ... existing code ...
    
    if fastapi_app:
        spec_target = name + "_openapi_spec"
        openapi_spec(
            name = spec_target,
            binary = binary_target,
            module_path = fastapi_app.split(":")[0],
            app_variable = fastapi_app.split(":")[1] if ":" in fastapi_app else "app",
        )
        
        # Optionally generate client
        if generate_client:
            client_ns = client_namespace or domain
            openapi_client(
                name = name + "_client",
                spec = ":" + spec_target,
                namespace = client_ns,
                app = name,
            )
```

## Testing Strategy

### 1. Build Verification
```bash
# Build all clients
bazel build //clients:all_clients

# Verify generated structure
ls -la bazel-bin/clients/external/manman/experience_api/
ls -la bazel-bin/clients/external/demo/hello_fastapi/
```

### 2. Import Testing
```bash
# Create test script
cat > /tmp/test_imports.py << 'EOF'
import sys
sys.path.insert(0, "bazel-bin/clients")

from external.manman.experience_api import DefaultApi
from external.manman.status_api import DefaultApi as StatusApi
from external.demo.hello_fastapi import DefaultApi as HelloApi

print("✓ All imports successful")
EOF

python /tmp/test_imports.py
```

### 3. Runtime Testing
```bash
# Run a service that uses the generated client
bazel run //manman/src/worker -- --test-client-import
```

### 4. CI Integration
Add to `.github/workflows/test.yml`:
```yaml
- name: Build OpenAPI Clients
  run: bazel build //clients:all_clients

- name: Test Client Imports
  run: |
    python -c "import sys; sys.path.insert(0, 'bazel-bin/clients'); from external.manman.experience_api import DefaultApi"
```

## Migration Path

### Phase 1: Setup (Day 1)
1. Create `openapi_client.bzl` rule
2. Add OpenAPI Generator dependency to MODULE.bazel
3. Test with single client (demo/hello_fastapi)

### Phase 2: Generate All Clients (Day 1-2)
1. Add all client targets to `//clients/BUILD.bazel`
2. Verify all clients build successfully
3. Commit generated client targets

### Phase 3: Migrate Consumers (Day 2-3)
1. Identify services making manual API calls
2. Replace with generated clients one service at a time
3. Update BUILD.bazel dependencies

### Phase 4: Documentation & Tooling (Day 3)
1. Update AGENTS.md with client generation pattern
2. Add IDE configuration examples
3. Document common usage patterns

## Common Patterns

### Synchronous vs Asynchronous
OpenAPI Generator Python client supports both:

```python
# Synchronous (default)
from external.manman.status_api import DefaultApi, ApiClient, Configuration

config = Configuration(host="http://status-api")
with ApiClient(config) as client:
    api = DefaultApi(client)
    result = api.get_worker(worker_id)

# Asynchronous (if async support enabled in generator)
async with ApiClient(config) as client:
    api = DefaultApi(client)
    result = await api.get_worker(worker_id)
```

### Error Handling
```python
from external.manman.status_api import DefaultApi, ApiException

try:
    api = DefaultApi(client)
    result = api.get_worker(worker_id)
except ApiException as e:
    if e.status == 404:
        print("Worker not found")
    elif e.status == 500:
        print("Server error")
    raise
```

### Configuration Management
```python
# Centralized API configuration
from external.manman.status_api import Configuration

def get_api_config():
    config = Configuration()
    config.host = os.getenv("STATUS_API_URL", "http://status-api")
    config.access_token = os.getenv("API_TOKEN")
    return config
```

## Circular Dependencies

### The Problem

In microservices architectures, circular dependencies can occur when:
- Service A needs to call Service B's API (depends on B's client)
- Service B needs to call Service A's API (depends on A's client)

In Bazel, this creates a build dependency cycle that prevents compilation.

### Why This Approach Handles It

**Key Insight**: Clients are generated in `//clients/`, completely separate from the service implementations.

```
//manman/src/host:experience_api_binary  ──→  //clients:manman_status_api
                                               (client for status API)
                ↑                                       ↓
                │                                       │
                │                              //manman/src/host:status_api_spec
                │                                       ↓
                └────────────────────────  //manman/src/host:status_api_binary
```

**This works because:**
1. **Spec generation is one-way**: `status_api_binary` → `status_api_spec` (no reverse dependency)
2. **Clients depend on specs**: `manman_status_api_client` → `status_api_spec` (not the binary)
3. **Services depend on clients**: `experience_api_binary` → `manman_status_api_client` (not the status binary)

### Dependency Flow

```
Service A Implementation (//services:service_a)
    ↓
Service A Binary (//services:service_a_bin)
    ↓
Service A Spec (//services:service_a_spec)  ←─── Generated from binary
    ↓
Service B Client (//clients:service_a_client)  ←─── Generated from spec
    ↓
Service B Implementation (//services:service_b)  ←─── Uses A's client
    ↓
Service B Binary (//services:service_b_bin)
    ↓
Service B Spec (//services:service_b_spec)
    ↓
Service A Client (//clients:service_b_client)  ←─── Generated from spec
    ↓
Service A Implementation (//services:service_a)  ←─── Uses B's client (back to top)
```

**No cycle exists because:**
- Service A implementation → Service B client (not Service B implementation)
- Service B implementation → Service A client (not Service A implementation)
- Clients are independent artifacts in `//clients/`

### Example: ManMan Experience ↔ Status APIs

```starlark
# //manman/src/host/BUILD.bazel

# Experience API can depend on Status API client
py_binary(
    name = "experience_api",
    srcs = ["experience_api.py"],
    deps = [
        "//clients:manman_status_api",  # ✓ No cycle
    ],
)

# Status API can depend on Experience API client
py_binary(
    name = "status_api",
    srcs = ["status_api.py"],
    deps = [
        "//clients:manman_experience_api",  # ✓ No cycle
    ],
)
```

```starlark
# //clients/BUILD.bazel

# Status API client depends on Status API spec (not binary)
openapi_client(
    name = "manman_status_api",
    spec = "//manman/src/host:status_api_spec",  # ✓ No cycle - spec, not binary
    namespace = "manman",
    app = "status_api",
)

# Experience API client depends on Experience API spec (not binary)
openapi_client(
    name = "manman_experience_api",
    spec = "//manman/src/host:experience_api_spec",  # ✓ No cycle - spec, not binary
    namespace = "manman",
    app = "experience_api",
)
```

### What If Specs Needed Binary Execution?

Even though specs are generated by introspecting the binary (via `app.openapi()`), the Bazel dependency is one-way:

```
Binary (source code) → Spec Generation Tool → Spec (JSON artifact)
```

The spec generation happens at **build time**, producing a static JSON file. The client generation then consumes this JSON file, not the binary itself.

### Runtime vs Build-Time

**Build-Time** (what matters for Bazel):
- Experience API code + Status API **client** → Experience API binary ✓
- Status API code + Experience API **client** → Status API binary ✓
- No circular dependency in the build graph

**Runtime**:
- Experience API calls Status API via HTTP (uses Status API client)
- Status API calls Experience API via HTTP (uses Experience API client)
- This is fine - it's just network communication

### Anti-Pattern to Avoid

**Don't do this** (would create actual cycle):
```starlark
py_binary(
    name = "service_a",
    deps = [
        "//services:service_b",  # ✗ Depending on another service's binary
    ],
)
```

**Do this instead**:
```starlark
py_binary(
    name = "service_a",
    deps = [
        "//clients:service_b_client",  # ✓ Depending on service B's client
    ],
)
```

### Verification

Test that circular dependencies work:

```bash
# Both services should build successfully
bazel build //manman/src/host:experience_api
bazel build //manman/src/host:status_api

# Both clients should build successfully
bazel build //clients:manman_experience_api
bazel build //clients:manman_status_api

# Check for cycles in dependency graph
bazel query "somepath(//manman/src/host:experience_api, //manman/src/host:status_api)"
# Should show path through clients, not direct dependency
```

## Benefits

1. **Type Safety**: Generated clients provide type hints and validation
2. **Auto-Documentation**: IDEs can provide inline docs from OpenAPI specs
3. **Consistency**: All API clients follow same patterns
4. **Maintainability**: Changes to API automatically reflected in clients
5. **Clear Separation**: `external/` makes it obvious what's generated
6. **Clean Imports**: `from external.namespace.app` is intuitive
7. **Circular Dependencies**: Clients as separate artifacts prevent build cycles

## Future Enhancements

1. **Multi-Language Support**: Generate clients for Go, TypeScript, etc.
2. **Versioned Clients**: Support multiple API versions side-by-side
3. **Client Testing**: Auto-generate client tests from OpenAPI examples
4. **Mock Generation**: Generate mock servers for testing
5. **SDK Publishing**: Publish clients as standalone packages to PyPI

## References

- [OpenAPI Generator Docs](https://openapi-generator.tech/docs/generators/python)
- [rules_python Documentation](https://github.com/bazelbuild/rules_python)
- [Bazel genrule Reference](https://bazel.build/reference/be/general#genrule)
- Proven Pattern: [friendly-computing-machine repository](https://github.com/whale-net/friendly-computing-machine) uses same approach

---

**Status**: Ready for implementation  
**Priority**: High (enables type-safe API consumption)  
**Estimated Effort**: 2-3 days for full implementation and migration
