# ManMan API Clients

This module provides client wrappers for ManMan's internal APIs. These wrappers use the generated OpenAPI clients internally while providing a stable interface that matches the domain model structure.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Worker Code                                            │
│  └── Uses: manman.clients.WorkerDALClient              │
│      (Generated OpenAPI client + model translation)    │
└─────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────┐
│  Worker DAL API                                         │
│  └── FastAPI endpoints at dedicated domain              │
└─────────────────────────────────────────────────────────┘
                          ↑
┌─────────────────────────────────────────────────────────┐
│  Repository Layer (API)                                 │
│  └── Uses: manman.src.repository.api_client            │
│      (Manual client, no /workerdal prefix)             │
│      Cannot use generated client due to circular deps  │
└─────────────────────────────────────────────────────────┘
```

## Available Clients

### WorkerDALClient

Client for the Worker DAL API that provides data access for worker services.

**Usage:**
```python
from manman.clients import WorkerDALClient

# Initialize client (no /workerdal prefix - API has dedicated domain)
client = WorkerDALClient(
    base_url="http://dal.manman.local",
    access_token="optional-bearer-token"
)

# Create a worker
worker = client.create_worker()

# Get game server config
config = client.get_game_server_config(config_id)

# Create game server instance
instance = client.create_game_server_instance(
    game_server_config_id=config.game_server_config_id,
    worker_id=worker.worker_id
)

# Send heartbeats
client.heartbeat_worker(worker)
client.heartbeat_game_server_instance(instance.game_server_instance_id)
```

## Why Generated Client Wrapper?

The `WorkerDALClient` uses the generated OpenAPI client internally and provides:

- **Type Safety**: Full type checking with Pydantic model validation  
- **Automatic API Compatibility**: Generated from OpenAPI spec, stays in sync with API changes
- **Model Translation**: Converts between generated models and domain models
- **Stable Interface**: Domain models are source of truth, insulated from API changes

This approach works because the worker service has no circular dependencies with the API layer.

## Future Expansion

This module is designed to support multiple API clients:
- `ExperienceAPIClient` - For experience API consumers
- `StatusAPIClient` - For status API consumers
- Other internal API clients as needed

Each client follows the same pattern:
- Wraps generated OpenAPI client
- Translates between domain models and generated models
- Provides authentication and error handling
- Maintains stable interface for consumers
