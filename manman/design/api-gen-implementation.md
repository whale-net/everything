# OpenAPI Client Generation - ManMan Implementation Guide

This document provides a practical implementation guide for generating Python client libraries for ManMan APIs with shared models, tailored to this Bazel-based monorepo.

## 📋 Table of Contents

1. [Overview](#overview)
2. [Current State Analysis](#current-state-analysis)
3. [Architecture & Design](#architecture--design)
4. [Implementation Strategies](#implementation-strategies)
5. [Step-by-Step Implementation](#step-by-step-implementation)
6. [Build Integration](#build-integration)
7. [Future Cleanup & Migration Path](#future-cleanup--migration-path)

---

## Overview

### Goals

- Generate independent Python client libraries for each ManMan API (`experience-api`, `status-api`, `worker-dal-api`)
- Share common Pydantic models across all clients to avoid duplication
- Package clients as distributable wheels for external consumption
- Integrate with existing Bazel build system
- Provide a clear migration path from duplicative to shared models

### Key APIs in ManMan

Based on the codebase, we have three main APIs:

| API Name | Purpose | Current Status | Client Needed |
|----------|---------|----------------|---------------|
| **Experience API** (`/experience`) | Main worker-facing API for game server management | Has endpoints | Yes |
| **Worker DAL API** (`/workerdal`) | Data access layer for worker operations | Has manual client (`api_client.py`) | Yes |
| **Status API** (`/status`) | Read-only status queries | Simple status endpoints | Yes |

---

## Current State Analysis

### Existing Infrastructure

✅ **Already Available:**
- OpenAPI spec generation via `manman/src/host/openapi.py`
- Centralized models in `manman/src/models.py` (Pydantic/SQLModel)
- FastAPI apps properly structured with factory functions
- Manual API client implementation (`api_client.py`) showing patterns

```bash
# Current command to generate OpenAPI specs
python -m manman.src.host.openapi experience-api
python -m manman.src.host.openapi status-api
python -m manman.src.host.openapi worker-dal-api
```

### Shared Models

All core models are defined in `manman/src/models.py`:

```python
# From manman/src/models.py
- Worker
- GameServerInstance
- GameServer
- GameServerConfig
- Command, CommandType
- StatusType, InternalStatusInfo, ExternalStatusInfo
- ManManBase (base class)
```

These are **already shared** across all APIs via direct imports. The challenge is ensuring generated clients use these same classes.

### Current Manual Client

The existing `WorkerAPIClient` in `api_client.py` demonstrates:
- How clients should deserialize responses into model classes
- Authentication patterns (currently disabled, but structured)
- Base URL management and routing

**Key Comment:** Line 196 says `# TODO - is there a way to auto generate this?` - **This is exactly what we're solving!**

---

## Architecture & Design

### Option A: Pragmatic Approach (Recommended First)

**Strategy:** Generate clients WITH duplicated models initially, then incrementally deduplicate.

**Pros:**
- Fast to implement
- Low risk - codegen is isolated
- Can validate generated code quality before tackling deduplication
- Works immediately with standard OpenAPI tooling

**Cons:**
- Temporary model duplication
- Requires migration step later

### Option B: Pure Shared Models (Ideal End State)

**Strategy:** Generate clients that import from a shared `manman-models` package.

**Pros:**
- No duplication
- Type safety across client/server
- Single source of truth

**Cons:**
- More complex setup
- Requires customizing OpenAPI generator config
- May need custom templates
- Harder to package for external distribution

---

## Implementation Strategies

### Strategy 1: Quick Win with Duplication (Phase 1)

This gets clients working **immediately** with minimal complexity.

#### Steps

1. **Generate specs** (already works)
   ```bash
   python -m manman.src.host.openapi experience-api
   python -m manman.src.host.openapi status-api  
   python -m manman.src.host.openapi worker-dal-api
   ```

2. **Generate clients with standard OpenAPI generator**
   ```bash
   openapi-generator-cli generate \
     -i openapi-specs/experience-api.json \
     -g python \
     -o clients/experience-api-client \
     --package-name manman_experience_client \
     --additional-properties=projectName=manman-experience-client
   ```

3. **Package and distribute**
   ```bash
   cd clients/experience-api-client
   python -m build
   # Creates: dist/manman_experience_client-1.0.0-py3-none-any.whl
   ```

4. **Use the client**
   ```python
   pip install manman-experience-client
   
   from manman_experience_client import ApiClient, DefaultApi
   from manman_experience_client.models import Worker
   
   client = DefaultApi(ApiClient(configuration=...))
   worker = client.worker_current()  # Returns Worker model
   ```

**Note:** Models are duplicated in each client package, but functionality works immediately.

### Strategy 2: Shared Models with Import Mapping (Phase 2)

This eliminates duplication by making generated clients import from shared models.

#### Key Concept

OpenAPI Generator's `importMappings` and `typeMappings` tell it:
- "Don't generate a `Worker` class"
- "Instead, import it from `manman.src.models`"

#### Configuration Example

```json
{
  "packageName": "manman_experience_client",
  "projectName": "manman-experience-client",
  "packageVersion": "1.0.0",
  "importMappings": {
    "Worker": "manman.src.models.Worker",
    "GameServerInstance": "manman.src.models.GameServerInstance",
    "GameServerConfig": "manman.src.models.GameServerConfig",
    "Command": "manman.src.models.Command",
    "ExternalStatusInfo": "manman.src.models.ExternalStatusInfo"
  },
  "typeMappings": {
    "Worker": "Worker",
    "GameServerInstance": "GameServerInstance",
    "GameServerConfig": "GameServerConfig",
    "Command": "Command",
    "ExternalStatusInfo": "ExternalStatusInfo"
  }
}
```

#### Self-Contained Packaging

For clients to work outside the monorepo, they need the model source code:

```bash
# After generating client, copy models into it
cp -r manman/src/models.py clients/experience-api-client/manman_experience_client/models/
```

This makes the import `from manman.src.models import Worker` resolve correctly when the wheel is installed elsewhere.

---

## Step-by-Step Implementation

### Phase 1: Basic Code Generation (No Shared Models)

**Goal:** Get working clients with duplication, establish build pipeline.

#### 1.1 Create Generation Script

Create `tools/generate_clients.py`:

```python
#!/usr/bin/env python3
"""
Generate Python clients for ManMan APIs.
"""
import json
import subprocess
from pathlib import Path
from typing import Literal

API_NAMES = ["experience-api", "status-api", "worker-dal-api"]

def generate_openapi_spec(api_name: str) -> Path:
    """Generate OpenAPI spec for an API."""
    print(f"Generating OpenAPI spec for {api_name}...")
    subprocess.run(
        ["python", "-m", "manman.src.host.openapi", api_name],
        check=True
    )
    return Path(f"openapi-specs/{api_name}.json")

def generate_client(api_name: str, spec_path: Path) -> Path:
    """Generate Python client from OpenAPI spec."""
    output_dir = Path(f"clients/{api_name}-client")
    package_name = f"manman_{api_name.replace('-', '_')}_client"
    
    print(f"Generating client for {api_name}...")
    subprocess.run([
        "openapi-generator-cli", "generate",
        "-i", str(spec_path),
        "-g", "python",
        "-o", str(output_dir),
        "--package-name", package_name,
        "--additional-properties",
        f"projectName=manman-{api_name}-client,packageVersion=0.1.0"
    ], check=True)
    
    return output_dir

def main():
    for api_name in API_NAMES:
        spec_path = generate_openapi_spec(api_name)
        client_dir = generate_client(api_name, spec_path)
        print(f"✅ Client generated: {client_dir}")

if __name__ == "__main__":
    main()
```

#### 1.2 Add Build Target

Create `clients/BUILD.bazel`:

```python
load("@rules_python//python:defs.bzl", "py_binary")

py_binary(
    name = "generate_clients",
    srcs = ["//tools:generate_clients.py"],
    main = "//tools:generate_clients.py",
    deps = [
        "//manman/src/host:manman_host",
    ],
    visibility = ["//visibility:public"],
)
```

#### 1.3 Run Generation

```bash
bazel run //clients:generate_clients
```

#### 1.4 Package Clients

```bash
cd clients/experience-api-client
python -m build
# Creates distributable wheel
```

### Phase 2: Shared Models Implementation

**Goal:** Eliminate duplication by using shared models.

#### 2.1 Extract Shared Models Package

First, we need models available as an importable package:

Option A: Keep in `manman/src/models.py` (current state)
Option B: Extract to `libs/python/manman_models/` (cleaner for distribution)

**Recommended: Option B**

Create `libs/python/manman_models/`:

```
libs/python/manman_models/
├── BUILD.bazel
├── __init__.py
├── models.py  # Copied from manman/src/models.py
└── pyproject.toml  # For packaging
```

Update `manman/src/models.py` to re-export:
```python
# For backward compatibility
from libs.python.manman_models.models import *
```

#### 2.2 Enhanced Generation Script

Update `tools/generate_clients.py`:

```python
import inspect
from typing import get_type_hints
from manman.src import models as manman_models

def discover_shared_models() -> dict[str, str]:
    """
    Automatically discover all Pydantic models in manman.src.models.
    Returns a dict mapping model name to full import path.
    """
    model_mappings = {}
    
    for name, obj in inspect.getmembers(manman_models):
        if inspect.isclass(obj) and hasattr(obj, 'model_validate'):
            # It's a Pydantic model
            model_mappings[name] = f"manman.src.models.{name}"
    
    return model_mappings

def generate_config_with_shared_models(
    api_name: str, 
    shared_models: dict[str, str]
) -> Path:
    """Generate OpenAPI generator config with import mappings."""
    config = {
        "packageName": f"manman_{api_name.replace('-', '_')}_client",
        "projectName": f"manman-{api_name}-client",
        "packageVersion": "0.1.0",
        "importMappings": shared_models,
        "typeMappings": {name: name for name in shared_models.keys()},
    }
    
    config_path = Path(f"tmp/openapi-config-{api_name}.json")
    config_path.parent.mkdir(exist_ok=True)
    
    with open(config_path, 'w') as f:
        json.dump(config, f, indent=2)
    
    return config_path

def copy_models_to_client(client_dir: Path):
    """Copy shared models source into client for self-containment."""
    import shutil
    
    # Copy models.py into the client package
    src = Path("manman/src/models.py")
    dest = client_dir / "manman" / "src"
    dest.mkdir(parents=True, exist_ok=True)
    
    shutil.copy(src, dest / "models.py")
    
    # Create __init__.py files for proper package structure
    (dest.parent / "__init__.py").touch()
    (dest / "__init__.py").touch()

def generate_client_with_shared_models(
    api_name: str, 
    spec_path: Path
) -> Path:
    """Generate client that uses shared models."""
    shared_models = discover_shared_models()
    config_path = generate_config_with_shared_models(api_name, shared_models)
    output_dir = Path(f"clients/{api_name}-client")
    
    print(f"Generating client for {api_name} with shared models...")
    subprocess.run([
        "openapi-generator-cli", "generate",
        "-i", str(spec_path),
        "-g", "python",
        "-c", str(config_path),
        "-o", str(output_dir),
    ], check=True)
    
    # Copy model source for self-containment
    copy_models_to_client(output_dir)
    
    return output_dir
```

#### 2.3 Usage

```bash
# Generate all clients with shared models
bazel run //clients:generate_clients

# Clients now import from manman.src.models instead of duplicating
```

#### 2.4 Verify No Duplication

```bash
# Check generated client
cat clients/experience-api-client/manman_experience_client/api/default_api.py

# Should see:
from manman.src.models import Worker  # Not from local models!
```

---

## Build Integration

### Bazel Integration

Create `clients/BUILD.bazel`:

```python
load("@rules_python//python:defs.bzl", "py_binary")

# Generation script
py_binary(
    name = "generate_clients",
    srcs = ["//tools:generate_clients.py"],
    main = "//tools:generate_clients.py",
    deps = [
        "//manman/src/host:manman_host",
    ],
)

# Generate specs and clients
genrule(
    name = "openapi_clients",
    srcs = [
        "//manman/src/host:openapi",
        "//manman/src:models",
    ],
    outs = [
        "experience-api-client/setup.py",
        "status-api-client/setup.py",
        "worker-dal-api-client/setup.py",
    ],
    cmd = "$(location :generate_clients) && cp -r clients/* $(@D)",
    tools = [":generate_clients"],
    visibility = ["//visibility:public"],
)
```

### CI/CD Integration

Add to `.github/workflows/client-generation.yml`:

```yaml
name: Generate API Clients

on:
  push:
    paths:
      - 'manman/src/host/api/**'
      - 'manman/src/models.py'

jobs:
  generate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      
      - name: Install OpenAPI Generator
        run: |
          wget https://repo1.maven.org/maven2/org/openapitools/openapi-generator-cli/7.1.0/openapi-generator-cli-7.1.0.jar -O openapi-generator-cli.jar
          alias openapi-generator-cli="java -jar openapi-generator-cli.jar"
      
      - name: Generate Clients
        run: bazel run //clients:generate_clients
      
      - name: Build Wheels
        run: |
          cd clients/experience-api-client && python -m build
          cd ../status-api-client && python -m build
          cd ../worker-dal-api-client && python -m build
      
      - name: Upload Artifacts
        uses: actions/upload-artifact@v3
        with:
          name: client-wheels
          path: clients/*/dist/*.whl
```

---

## Future Cleanup & Migration Path

### From Duplicated to Shared Models

If you start with **Strategy 1** (duplication), here's how to migrate to **Strategy 2** (shared):

#### Migration Checklist

1. **Extract models to separate package**
   ```bash
   mkdir -p libs/python/manman_models
   cp manman/src/models.py libs/python/manman_models/
   ```

2. **Update generation config**
   - Add `importMappings` for each model
   - Add `typeMappings` for each model

3. **Regenerate clients**
   ```bash
   bazel run //clients:generate_clients
   ```

4. **Verify imports**
   ```bash
   grep -r "from manman.src.models" clients/*/manman_*_client/
   # Should find imports, not local definitions
   ```

5. **Update consumers**
   - Old: `from manman_experience_client.models import Worker`
   - New: `from manman.src.models import Worker` (same as server)

6. **Deprecate old clients**
   - Publish new major version
   - Add deprecation warnings to old packages

### Model Versioning Strategy

To handle breaking changes in models:

```python
# libs/python/manman_models/v1/models.py
class Worker(SQLModel):
    worker_id: int
    # v1 fields

# libs/python/manman_models/v2/models.py  
class Worker(SQLModel):
    worker_id: int
    # v2 fields (breaking changes)
```

Generate clients for each version:
- `manman-experience-client-v1` uses `v1.models`
- `manman-experience-client-v2` uses `v2.models`

---

## Testing Generated Clients

### Unit Tests

Create `clients/tests/test_experience_client.py`:

```python
import pytest
from manman_experience_client import ApiClient, DefaultApi
from manman.src.models import Worker

def test_worker_model_is_shared():
    """Verify generated client uses shared models, not duplicated ones."""
    from manman_experience_client.api.default_api import DefaultApi
    import inspect
    
    # Get return annotation for worker_current method
    sig = inspect.signature(DefaultApi.worker_current)
    return_type = sig.return_annotation
    
    # Should be the shared Worker class from manman.src.models
    assert return_type is Worker
    assert Worker.__module__ == "manman.src.models"

def test_client_initialization():
    """Test client can be initialized."""
    client = ApiClient()
    api = DefaultApi(client)
    assert api is not None
```

### Integration Tests

```python
@pytest.fixture
def test_server():
    """Start actual FastAPI test server."""
    from manman.src.host.api.experience import create_app
    from fastapi.testclient import TestClient
    
    app = create_app()
    return TestClient(app)

def test_generated_client_against_real_server(test_server):
    """Test generated client works with real API."""
    # Configure client to use test server
    config = Configuration(host="http://testserver")
    client = ApiClient(configuration=config)
    api = DefaultApi(client)
    
    # Make real API call
    worker = api.worker_current()
    
    assert isinstance(worker, Worker)
    assert worker.worker_id > 0
```

---

## Troubleshooting

### Common Issues

#### 1. OpenAPI Generator Not Found

```bash
# Install via npm
npm install @openapitools/openapi-generator-cli -g

# Or use Docker
docker run --rm -v ${PWD}:/local openapitools/openapi-generator-cli generate \
  -i /local/openapi-specs/experience-api.json \
  -g python \
  -o /local/clients/experience-api-client
```

#### 2. Import Errors in Generated Client

**Symptom:** `ModuleNotFoundError: No module named 'manman'`

**Fix:** Ensure models were copied into client package:
```bash
ls clients/experience-api-client/manman/src/models.py
# Should exist if using shared models approach
```

#### 3. Type Mismatches

**Symptom:** Client expects `WorkerDto` but server returns `Worker`

**Fix:** Ensure FastAPI routes use actual model classes:
```python
# Good
@router.get("/worker/current")
async def worker_current() -> Worker:  # Actual model
    ...

# Bad  
@router.get("/worker/current")
async def worker_current() -> dict:  # Generic dict
    ...
```

#### 4. Circular Imports

**Symptom:** `ImportError: cannot import name 'Worker' from partially initialized module`

**Fix:** Restructure models to avoid circular dependencies, use `TYPE_CHECKING`:
```python
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .game_server import GameServer

class Worker(SQLModel):
    game_servers: list["GameServer"] = []  # Forward reference
```

---

## Summary

### Recommended Implementation Path

1. **Start with Phase 1** (duplication)
   - Get clients working quickly
   - Validate OpenAPI specs are correct
   - Test packaging and distribution

2. **Move to Phase 2** (shared models)
   - Extract models to separate package
   - Configure import mappings
   - Regenerate clients
   - Verify no duplication

3. **Integrate with Bazel**
   - Add build targets
   - Automate generation
   - Add to CI/CD

4. **Replace manual client**
   - Deprecate `api_client.py`
   - Migrate to generated clients
   - Remove manual code

### Key Benefits

✅ **Type Safety:** Clients and server share exact model definitions  
✅ **DRY:** Single source of truth for API contracts  
✅ **Automation:** Regenerate clients automatically on API changes  
✅ **Distribution:** Package as wheels for external consumption  
✅ **Testability:** Generated clients can be tested against real servers  

### Next Steps

1. Review and validate approach with team
2. Install OpenAPI Generator tooling
3. Run Phase 1 generation for one API (worker-dal-api is best candidate)
4. Validate generated client works
5. Expand to all APIs
6. Migrate to shared models (Phase 2)
7. Replace manual `api_client.py` code
