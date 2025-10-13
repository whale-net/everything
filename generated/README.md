# Generated OpenAPI Clients

This directory contains generated OpenAPI client code for openapi-based services.

## Usage

### For Bazel Builds (Production)

In production and CI, use the Bazel targets directly:

```python
# In BUILD.bazel
py_binary(
    name = "my_app",
    deps = [
        "//generated/manman:experience_api",
        "//generated/manman:status_api",
    ],
)
```

### For Local Development (IDE Support)

For local development with IDE autocomplete, sync the generated files:

```bash
# Generate and copy clients to local directory
./tools/scripts/sync_generated_clients.sh
```

Then your IDE will find the imports at `generated/manman/`.


## Importing Clients
example:
```python
from generated.manman.experience_api import DefaultApi as ExperienceApi
from generated.manman.experience_api.api_client import ApiClient
from generated.manman.experience_api.configuration import Configuration

from generated.manman.status_api import DefaultApi as StatusApi
from generated.manman.worker_dal_api import DefaultApi as WorkerDalApi
```

## Regenerating Clients

Clients are automatically regenerated when the OpenAPI specs change:

```bash
# Rebuild all clients
bazel build //generated/manman:all

# Rebuild specific client
bazel build //generated/manman:experience_api

# Sync to local directory for IDE
./tools/scripts/sync_generated_clients.sh
```

## Git Ignore

Generated files are ignored by git (see `.gitignore`). Only BUILD.bazel files are tracked.

## CI/CD

In CI/CD pipelines, clients are generated on-demand by Bazel. No pre-generated files are needed in the repository.
