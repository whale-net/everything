# ManMan Target Dependency Graph

This document visualizes the dependency relationships between the granular targets.

## Module Overview

```
manman/
├── src/                                    (Core Module)
│   ├── manman_core_models                  [models, exceptions, constants]
│   ├── manman_core_config                  [configuration]
│   ├── manman_core_logging                 [logging setup]
│   ├── manman_core_utils                   [utility functions]
│   └── manman_core                         [aggregate]
│
├── src/repository/                         (Repository Module)
│   ├── manman_repository_database          [database access]
│   ├── manman_repository_rabbitmq          [RabbitMQ infrastructure]
│   ├── manman_repository_message           [pub/sub services]
│   ├── manman_repository_api_client        [external API client]
│   └── manman_repository                   [aggregate]
│
├── src/worker/                             (Worker Module)
│   ├── manman_worker_core                  [abstractions, utilities]
│   ├── manman_worker_server                [server management]
│   ├── manman_worker_service               [worker implementation]
│   ├── manman_worker_main                  [entry point]
│   └── manman_worker                       [aggregate]
│
└── src/host/                               (Host Module)
    ├── manman_host_shared                  [shared API utilities]
    ├── manman_host_experience_api          [Experience API]
    ├── manman_host_status_api              [Status API]
    ├── manman_host_worker_dal_api          [Worker DAL API]
    ├── manman_host_status_processor        [Status processor]
    ├── manman_host_main                    [CLI and app factories]
    └── manman_host                         [aggregate]
```

## Core Module Dependencies

```
manman_core_models
  └─ pydantic, sqlmodel

manman_core_config
  └─ python-dotenv

manman_core_logging
  └─ manman_core_config
  └─ opentelemetry-api, opentelemetry-sdk

manman_core_utils
  └─ manman_core_config
  └─ amqpstorm, sqlmodel, asyncpg, alembic

manman_core (aggregate)
  ├─ manman_core_models
  ├─ manman_core_config
  ├─ manman_core_logging
  ├─ manman_core_utils
  └─ typer, fastapi
```

## Repository Module Dependencies

```
manman_repository_database
  └─ manman_core
  └─ sqlmodel, asyncpg, alembic, opentelemetry-instrumentation-sqlalchemy

manman_repository_rabbitmq
  └─ manman_core
  └─ amqpstorm

manman_repository_message
  ├─ manman_repository_rabbitmq
  ├─ manman_core
  └─ amqpstorm

manman_repository_api_client
  └─ manman_core
  └─ httpx, requests, python-jose

manman_repository (aggregate)
  ├─ manman_repository_database
  ├─ manman_repository_rabbitmq
  ├─ manman_repository_message
  └─ manman_repository_api_client
```

## Worker Module Dependencies

```
manman_worker_core
  ├─ manman_core
  ├─ manman_repository
  └─ requests, python-jose

manman_worker_server
  ├─ manman_worker_core
  ├─ manman_core
  └─ manman_repository

manman_worker_service
  ├─ manman_worker_core
  ├─ manman_worker_server
  ├─ manman_core
  ├─ manman_repository
  └─ amqpstorm

manman_worker_main
  ├─ manman_worker_service
  ├─ manman_worker_core
  ├─ manman_core
  ├─ manman_repository
  └─ typer, amqpstorm

manman_worker (aggregate)
  ├─ manman_worker_main
  ├─ manman_worker_service
  ├─ manman_worker_server
  └─ manman_worker_core
```

## Host Module Dependencies

```
manman_host_shared
  ├─ manman_core
  ├─ manman_repository
  └─ fastapi, python-jose

manman_host_experience_api
  ├─ manman_host_shared
  ├─ manman_core
  ├─ manman_repository
  └─ fastapi

manman_host_status_api
  ├─ manman_host_shared
  ├─ manman_core
  ├─ manman_repository
  └─ fastapi

manman_host_worker_dal_api
  ├─ manman_host_shared
  ├─ manman_core
  ├─ manman_repository
  └─ fastapi

manman_host_status_processor
  ├─ manman_core
  ├─ manman_repository
  └─ amqpstorm

manman_host_main
  ├─ manman_host_shared
  ├─ manman_host_experience_api
  ├─ manman_host_status_api
  ├─ manman_host_worker_dal_api
  ├─ manman_host_status_processor
  ├─ manman_core
  ├─ manman_repository
  ├─ manman_migrations
  └─ fastapi, uvicorn, gunicorn, typer, alembic, opentelemetry-instrumentation-fastapi

manman_host (aggregate)
  ├─ manman_host_main
  ├─ manman_host_shared
  ├─ manman_host_experience_api
  ├─ manman_host_status_api
  ├─ manman_host_worker_dal_api
  └─ manman_host_status_processor
```

## Binary Targets

```
//manman/src/host:experience_api
  └─ manman_host_main

//manman/src/host:status_api
  └─ manman_host_main

//manman/src/host:worker_dal_api
  └─ manman_host_main

//manman/src/host:status_processor
  └─ manman_host_main

//manman/src/host:migration
  └─ manman_host_main

//manman/src/worker:worker
  └─ manman_worker_main
```

## Test Targets

### Core Module Tests
- `config_test` → manman_core_config
- `models_test` → manman_core_models
- `simple_status_test` → manman_core_models
- `manman_core_test` → manman_core (aggregate)

### Repository Module Tests
- `database_repository_test` → manman_repository_database
- `validator_test` → manman_repository_database
- `rabbitmq_util_test` → manman_repository_rabbitmq
- `manman_repository_test` → manman_repository (aggregate)

### Worker Module Tests
- `server_status_test` → manman_worker_server, manman_worker_core
- `worker_service_test` → manman_worker_service, manman_worker_core
- `manman_worker_test` → manman_worker (aggregate)

### Host Module Tests
- `experience_api_test` → manman_host_experience_api, manman_host_shared
- `status_api_test` → manman_host_status_api, manman_host_shared
- `worker_dal_api_test` → manman_host_worker_dal_api, manman_host_shared
- `status_processor_test` → manman_host_status_processor
- `manman_host_test` → manman_host (aggregate)

## Dependency Levels

The targets follow a clear layering:

```
Level 0 (External Dependencies):
  └─ PyPI packages (pydantic, fastapi, sqlmodel, etc.)

Level 1 (Core):
  ├─ manman_core_models
  ├─ manman_core_config
  ├─ manman_core_logging
  └─ manman_core_utils

Level 2 (Core Aggregate):
  └─ manman_core

Level 3 (Repository Components):
  ├─ manman_repository_database
  ├─ manman_repository_rabbitmq
  ├─ manman_repository_message
  └─ manman_repository_api_client

Level 4 (Repository Aggregate):
  └─ manman_repository

Level 5 (Worker Components):
  ├─ manman_worker_core
  ├─ manman_worker_server
  └─ manman_worker_service

Level 6 (Host Components):
  ├─ manman_host_shared
  ├─ manman_host_experience_api
  ├─ manman_host_status_api
  ├─ manman_host_worker_dal_api
  └─ manman_host_status_processor

Level 7 (Entry Points):
  ├─ manman_worker_main
  └─ manman_host_main

Level 8 (Aggregates):
  ├─ manman_worker
  └─ manman_host

Level 9 (Binaries):
  ├─ experience_api
  ├─ status_api
  ├─ worker_dal_api
  ├─ status_processor
  ├─ migration
  └─ worker
```

## Cross-Module Dependencies

Some key cross-module dependencies:

1. **All modules depend on `manman_core`** (or its sub-components)
2. **Worker and Host depend on `manman_repository`** (or its sub-components)
3. **Host APIs share `manman_host_shared`**
4. **Worker services build on `manman_worker_core`**

## Circular Dependency Prevention

The granular structure prevents circular dependencies by:
1. Clear layering (Level 0-9)
2. One-way dependencies (lower → higher levels only)
3. Aggregate libraries only depend on component libraries, never vice versa
4. Shared utilities separated into distinct libraries

## Target Count Summary

- **Core Module**: 5 libraries + 4 tests
- **Repository Module**: 5 libraries + 4 tests
- **Worker Module**: 5 libraries + 1 binary + 3 tests
- **Host Module**: 7 libraries + 5 binaries + 5 tests
- **Total**: 22 libraries, 6 binaries, 16 tests

Compare to before:
- **Before**: 4 monolithic libraries
- **After**: 22 granular libraries + 4 aggregate libraries (backward compatibility)
