# ManMan Module Refactoring Summary

## What Changed

### Before: Monolithic Structure

The ManMan module consisted of 4 large, monolithic library targets:

```
manman/
├── src/
│   └── manman_core                 [All core functionality in one target]
├── src/repository/
│   └── manman_repository           [All repository code in one target]
├── src/worker/
│   └── manman_worker              [All worker code in one target]
└── src/host/
    └── manman_host                [All host code in one target]
```

**Problems with monolithic structure:**
- 🐢 **Slow builds**: Changing any file rebuilds entire module
- 🔗 **Unclear dependencies**: Hard to see what depends on what
- 📦 **Large targets**: Each library pulls in all dependencies
- 🔄 **Cascading rebuilds**: Small change triggers massive rebuilds

### After: Granular Structure

The ManMan module now has 22 focused library targets organized by functionality:

```
manman/
├── src/                                    [Core Module - 5 targets]
│   ├── manman_core_models                  ← Data models
│   ├── manman_core_config                  ← Configuration
│   ├── manman_core_logging                 ← Logging
│   ├── manman_core_utils                   ← Utilities
│   └── manman_core                         ← Aggregate (backward compat)
│
├── src/repository/                         [Repository Module - 5 targets]
│   ├── manman_repository_database          ← Database access
│   ├── manman_repository_rabbitmq          ← RabbitMQ
│   ├── manman_repository_message           ← Pub/Sub
│   ├── manman_repository_api_client        ← External APIs
│   └── manman_repository                   ← Aggregate (backward compat)
│
├── src/worker/                             [Worker Module - 5 targets]
│   ├── manman_worker_core                  ← Abstractions
│   ├── manman_worker_server                ← Server mgmt
│   ├── manman_worker_service               ← Worker impl
│   ├── manman_worker_main                  ← Entry point
│   └── manman_worker                       ← Aggregate (backward compat)
│
└── src/host/                               [Host Module - 7 targets]
    ├── manman_host_shared                  ← Shared utils
    ├── manman_host_experience_api          ← Experience API
    ├── manman_host_status_api              ← Status API
    ├── manman_host_worker_dal_api          ← Worker DAL API
    ├── manman_host_status_processor        ← Status processor
    ├── manman_host_main                    ← CLI & factories
    └── manman_host                         ← Aggregate (backward compat)
```

**Benefits of granular structure:**
- ⚡ **Fast builds**: Only rebuild changed components
- 🎯 **Clear dependencies**: Explicit module relationships
- 📦 **Small targets**: Each library has minimal dependencies
- 🔄 **Targeted rebuilds**: Changes only rebuild affected code

## Target Statistics

| Metric | Before | After | Change |
|--------|--------|-------|--------|
| **Library targets** | 4 | 22 | +18 (+450%) |
| **Test targets** | 4 | 16 | +12 (+300%) |
| **Binary targets** | 6 | 6 | No change |
| **Lines in BUILD files** | ~100 | ~500 | +400 (+400%) |

## Detailed Changes

### 1. Core Module (`//manman/src`)

**Split into 4 focused libraries:**

| Target | Files | Purpose |
|--------|-------|---------|
| `manman_core_models` | models.py, exceptions.py, constants.py | Data structures |
| `manman_core_config` | config.py | Configuration |
| `manman_core_logging` | logging_config.py | Logging setup |
| `manman_core_utils` | util.py | Utility functions |

**Tests split into 3 focused tests:**
- `config_test` - Configuration testing
- `models_test` - Model validation
- `simple_status_test` - Status functionality

### 2. Repository Module (`//manman/src/repository`)

**Split into 4 focused libraries:**

| Target | Files | Purpose |
|--------|-------|---------|
| `manman_repository_database` | database.py, workerdal.py | Database access |
| `manman_repository_rabbitmq` | rabbitmq/* | RabbitMQ infrastructure |
| `manman_repository_message` | message/* | Message pub/sub |
| `manman_repository_api_client` | api_client.py | External API calls |

**Tests split into 3 focused tests:**
- `database_repository_test` - Database testing
- `validator_test` - Validation testing
- `rabbitmq_util_test` - RabbitMQ testing

### 3. Worker Module (`//manman/src/worker`)

**Split into 4 focused libraries:**

| Target | Files | Purpose |
|--------|-------|---------|
| `manman_worker_core` | abstract_service.py, processbuilder.py, steamcmd.py | Core abstractions |
| `manman_worker_server` | server.py | Server management |
| `manman_worker_service` | worker_service.py | Worker implementation |
| `manman_worker_main` | main.py | Entry point |

**Tests split into 2 focused tests:**
- `server_status_test` - Server testing
- `worker_service_test` - Worker service testing

### 4. Host Module (`//manman/src/host`)

**Split into 6 focused libraries:**

| Target | Files | Purpose |
|--------|-------|---------|
| `manman_host_shared` | api/shared/*, api/__init__.py, api/request_models.py | Shared utilities |
| `manman_host_experience_api` | api/experience/* | Experience API |
| `manman_host_status_api` | api/status/* | Status API |
| `manman_host_worker_dal_api` | api/worker_dal/* | Worker DAL API |
| `manman_host_status_processor` | status_processor.py | Status processor |
| `manman_host_main` | main.py, openapi.py, __init__.py | CLI & app factories |

**Tests split into 4 focused tests:**
- `experience_api_test` - Experience API testing
- `status_api_test` - Status API testing
- `worker_dal_api_test` - Worker DAL API testing
- `status_processor_test` - Status processor testing

## Backward Compatibility

**100% backward compatible!** All existing code continues to work:

```python
# Old code (still works)
deps = [
    "//manman/src:manman_core",
    "//manman/src/repository:manman_repository",
    "//manman/src/worker:manman_worker",
    "//manman/src/host:manman_host",
]
```

Each module has an aggregate library that includes all sub-components:
- `manman_core` → all core components
- `manman_repository` → all repository components
- `manman_worker` → all worker components
- `manman_host` → all host components

## New Usage Patterns

**For new code, use granular targets:**

```python
# New code (more efficient)
deps = [
    "//manman/src:manman_core_models",           # Only models
    "//manman/src/repository:manman_repository_database",  # Only database
]
```

**Benefits:**
- Faster builds (only rebuild what changed)
- Clearer dependencies (explicit requirements)
- Better isolation (changes don't cascade)

## Build Performance Comparison

### Scenario: Change a model in `models.py`

**Before (monolithic):**
```
1. manman_core rebuilds (all 8 files)
2. manman_repository rebuilds (depends on manman_core)
3. manman_worker rebuilds (depends on manman_core + manman_repository)
4. manman_host rebuilds (depends on manman_core + manman_repository)
Total: 4 large targets rebuilt
```

**After (granular):**
```
1. manman_core_models rebuilds (3 files)
2. Only targets that import models rebuild
3. Other core components (config, logging, utils) are cached
4. Repository/worker/host components that don't use the changed model are cached
Total: ~2-3 small targets rebuilt
```

**Estimated improvement: 50-70% faster rebuilds for typical changes**

## Documentation

Three new documentation files explain the refactoring:

1. **[REFACTORING.md](./REFACTORING.md)** - Complete refactoring guide
   - Overview and benefits
   - Detailed breakdown of each module
   - Migration guide
   - Testing strategy

2. **[TARGET_DEPENDENCIES.md](./TARGET_DEPENDENCIES.md)** - Dependency visualization
   - Module structure
   - Dependency graphs
   - Cross-module dependencies
   - Circular dependency prevention

3. **[README.md](./README.md)** - Updated with module structure
   - Quick reference to new structure
   - Links to detailed documentation

## Migration Guide

### For Existing Code
✅ **No action required!** Aggregate libraries provide full backward compatibility.

### For New Code
🎯 **Use granular targets** for better performance:

```python
# Instead of:
deps = ["//manman/src:manman_core"]

# Use:
deps = [
    "//manman/src:manman_core_models",  # Only what you need
    "//manman/src:manman_core_config",
]
```

### For Tests
🧪 **Use specific test targets** for faster test runs:

```bash
# Before: Test everything
bazel test //manman/src:manman_core_test

# After: Test only models
bazel test //manman/src:models_test

# Or test everything (still works)
bazel test //manman/src:manman_core_test
```

## Files Changed

### BUILD Files Modified (4 files)
- `manman/src/BUILD.bazel` - Core module targets
- `manman/src/repository/BUILD.bazel` - Repository module targets
- `manman/src/worker/BUILD.bazel` - Worker module targets
- `manman/src/host/BUILD.bazel` - Host module targets

### Documentation Added (3 files)
- `manman/REFACTORING.md` - Refactoring guide
- `manman/TARGET_DEPENDENCIES.md` - Dependency documentation
- `manman/README.md` - Updated README

### Total Changes
- **7 files modified/created**
- **~500 lines added** (mostly BUILD file definitions and documentation)
- **0 source code changes** (pure build system refactoring)

## Next Steps

After this PR is merged:

1. **Monitor build performance** - Track Bazel build times
2. **Encourage granular usage** - Update style guide to recommend granular targets
3. **Consider further decomposition** - Some targets could be split further if needed
4. **Enforce boundaries** - Use visibility constraints to prevent unwanted dependencies

## Conclusion

This refactoring provides:
- ✅ **Better build performance** through fine-grained caching
- ✅ **Clearer architecture** through explicit dependencies
- ✅ **Easier maintenance** through smaller, focused modules
- ✅ **100% backward compatibility** through aggregate libraries
- ✅ **Comprehensive documentation** for developers

The ManMan module is now structured for scalability and performance while maintaining complete backward compatibility with existing code.
