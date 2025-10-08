# ManMan Module Refactoring

This document describes the refactoring of the ManMan module from monolithic library targets into granular, focused library targets.

## Overview

The ManMan module has been restructured to break up large monolithic libraries into smaller, more focused targets. This improves:
- **Build performance**: Only rebuild what changed
- **Dependency clarity**: Clear understanding of what depends on what
- **Modularity**: Better separation of concerns
- **Maintainability**: Easier to understand and modify individual components

## Refactored Modules

### 1. Core Module (`//manman/src`)

**Before**: Single `manman_core` library with all core functionality

**After**: Four focused libraries plus an aggregate:

| Target | Purpose | Key Dependencies |
|--------|---------|------------------|
| `manman_core_models` | Data models, exceptions, constants | pydantic, sqlmodel |
| `manman_core_config` | Configuration management | python-dotenv |
| `manman_core_logging` | Logging configuration | opentelemetry-api, opentelemetry-sdk |
| `manman_core_utils` | Utility functions | amqpstorm, sqlmodel, asyncpg, alembic |
| `manman_core` | **Aggregate library** (backward compatibility) | All above |

**Individual Tests**:
- `config_test` - Tests for configuration
- `models_test` - Tests for data models
- `simple_status_test` - Tests for status functionality
- `manman_core_test` - Aggregate test (backward compatibility)

### 2. Repository Module (`//manman/src/repository`)

**Before**: Single `manman_repository` library with all repository functionality

**After**: Four focused libraries plus an aggregate:

| Target | Purpose | Key Dependencies |
|--------|---------|------------------|
| `manman_repository_database` | Database access layer | sqlmodel, asyncpg, alembic |
| `manman_repository_rabbitmq` | RabbitMQ infrastructure | amqpstorm |
| `manman_repository_message` | Message pub/sub services | manman_repository_rabbitmq, amqpstorm |
| `manman_repository_api_client` | External API client | httpx, requests, python-jose |
| `manman_repository` | **Aggregate library** (backward compatibility) | All above |

**Individual Tests**:
- `database_repository_test` - Tests for database layer
- `validator_test` - Tests for validation logic
- `rabbitmq_util_test` - Tests for RabbitMQ utilities
- `manman_repository_test` - Aggregate test (backward compatibility)

### 3. Worker Module (`//manman/src/worker`)

**Before**: Single `manman_worker` library with all worker functionality

**After**: Four focused libraries plus an aggregate:

| Target | Purpose | Key Dependencies |
|--------|---------|------------------|
| `manman_worker_core` | Core abstractions and utilities | manman_core, manman_repository |
| `manman_worker_server` | Server management functionality | manman_worker_core |
| `manman_worker_service` | Worker service implementation | manman_worker_core, manman_worker_server, amqpstorm |
| `manman_worker_main` | Main entry point | manman_worker_service, typer |
| `manman_worker` | **Aggregate library** (backward compatibility) | All above |

**Individual Tests**:
- `server_status_test` - Tests for server management
- `worker_service_test` - Tests for worker service
- `manman_worker_test` - Aggregate test (backward compatibility)

### 4. Host Module (`//manman/src/host`)

**Before**: Single `manman_host` library with all host functionality

**After**: Six focused libraries plus an aggregate:

| Target | Purpose | Key Dependencies |
|--------|---------|------------------|
| `manman_host_shared` | Shared API utilities | fastapi, python-jose |
| `manman_host_experience_api` | Experience API | manman_host_shared, fastapi |
| `manman_host_status_api` | Status API | manman_host_shared, fastapi |
| `manman_host_worker_dal_api` | Worker DAL API | manman_host_shared, fastapi |
| `manman_host_status_processor` | Status processor service | amqpstorm |
| `manman_host_main` | Main CLI and app factories | All host libraries, uvicorn, gunicorn, typer |
| `manman_host` | **Aggregate library** (backward compatibility) | All above |

**Individual Tests**:
- `experience_api_test` - Tests for Experience API
- `status_api_test` - Tests for Status API
- `worker_dal_api_test` - Tests for Worker DAL API
- `status_processor_test` - Tests for Status Processor
- `manman_host_test` - Aggregate test (backward compatibility)

## Migration Guide

### For Existing Code

**No changes required!** All existing dependencies on `manman_core`, `manman_repository`, `manman_worker`, and `manman_host` continue to work through the aggregate libraries.

### For New Code

When creating new code, prefer using the granular targets to minimize dependencies:

```python
# ❌ Old way (still works but pulls in everything)
deps = [
    "//manman/src:manman_core",
    "//manman/src/repository:manman_repository",
]

# ✅ New way (only depends on what you need)
deps = [
    "//manman/src:manman_core_models",
    "//manman/src/repository:manman_repository_database",
]
```

### For Tests

When writing tests, you can now depend only on the specific modules being tested:

```python
# ❌ Old way
py_test(
    name = "my_test",
    deps = [":manman_host"],  # Pulls in everything
)

# ✅ New way
py_test(
    name = "my_test",
    deps = [
        ":manman_host_experience_api",  # Only what's needed
        ":manman_host_shared",
    ],
)
```

## Benefits

### Build Performance

With granular targets, Bazel can:
- Cache builds at a finer granularity
- Parallelize builds more effectively
- Rebuild only changed components

Example: Changing a model in `manman_core_models` now only rebuilds code that depends on models, not all of `manman_core`.

### Dependency Clarity

The refactored structure makes dependencies explicit:

```
manman_host_experience_api
  └─ manman_host_shared
     └─ manman_core
        └─ manman_core_models
```

### Reduced Coupling

Each module now has a clear, minimal set of dependencies:
- API modules only depend on FastAPI and shared utilities
- Core modules are separated by concern (models, config, logging, utils)
- Repository modules are organized by data source (database, RabbitMQ, external APIs)

## Target Naming Convention

All targets follow a consistent naming pattern:

```
<module>_<submodule>_<component>
```

Examples:
- `manman_core_models` - Models component of core module
- `manman_repository_database` - Database component of repository module
- `manman_host_experience_api` - Experience API component of host module

Aggregate targets maintain the original names for backward compatibility:
- `manman_core`
- `manman_repository`
- `manman_worker`
- `manman_host`

## Testing Strategy

Each granular library has corresponding focused tests:

1. **Granular tests** - Test individual components
2. **Aggregate tests** - Test integration between components (backward compatibility)

This allows running:
```bash
# Test only models
bazel test //manman/src:models_test

# Test entire core module
bazel test //manman/src:manman_core_test

# Test all of manman
bazel test //manman/...
```

## Future Improvements

Potential enhancements to consider:

1. **Further decomposition**: Some modules (like `manman_host_shared`) could be split further if they grow
2. **Interface libraries**: Create pure interface libraries for better abstraction
3. **Dependency injection**: Use DI to reduce coupling between components
4. **Module boundaries**: Enforce visibility constraints to prevent circular dependencies

## Conclusion

This refactoring provides a solid foundation for the ManMan module to scale while maintaining backward compatibility. The granular structure enables better build performance, clearer dependencies, and improved maintainability.
