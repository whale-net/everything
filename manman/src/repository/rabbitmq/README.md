# DEPRECATED: RabbitMQ Implementation Moved

**This directory is deprecated.** The RabbitMQ implementation has been migrated to `//libs/python/rmq`.

## Migration

All RabbitMQ functionality has been moved to a reusable library at:
- **Location**: `libs/python/rmq/`
- **Bazel target**: `//libs/python/rmq`

## What Was Migrated

The following components are now available in `//libs/python/rmq`:

1. **Connection Management** (`connection.py`)
   - `init_rabbitmq()` - Initialize connection parameters
   - `get_rabbitmq_connection()` - Get/create per-process connections
   - `get_rabbitmq_ssl_options()` - Create SSL options
   - `cleanup_rabbitmq_connections()` - Graceful cleanup
   - `create_rabbitmq_vhost()` - Virtual host creation

2. **Configuration** (`config.py`)
   - `BindingConfig` - Exchange and routing key bindings
   - `QueueConfig` - Queue configuration
   - `RoutingKeyConfig` - Routing key builder
   - `ExchangeRegistry` - Exchange names (extend in your app)
   - `MessageTypeRegistry` - Message types (extend in your app)
   - `TopicWildcard` - Wildcard patterns

3. **Publisher/Subscriber** (`publisher.py`, `subscriber.py`)
   - `RabbitPublisher` - Message publisher
   - `RabbitSubscriber` - Message subscriber with threading

4. **Utilities** (`util.py`)
   - `add_routing_key_prefix()` - Add prefix to routing keys
   - `add_routing_key_suffix()` - Add suffix to routing keys

5. **Interfaces** (`interface.py`)
   - `MessagePublisherInterface` - Publisher abstract interface
   - `MessageSubscriberInterface` - Subscriber abstract interface

## Usage

Replace old imports:
```python
# OLD
from manman.src.repository.rabbitmq.config import (
    BindingConfig, ExchangeRegistry, QueueConfig, RoutingKeyConfig
)
from manman.src.repository.rabbitmq.publisher import RabbitPublisher
from manman.src.repository.rabbitmq.subscriber import RabbitSubscriber
from manman.src.util import get_rabbitmq_connection, init_rabbitmq

# NEW
from libs.python.rmq import (
    BindingConfig, ExchangeRegistry, QueueConfig, RoutingKeyConfig,
    RabbitPublisher, RabbitSubscriber,
    get_rabbitmq_connection, init_rabbitmq
)
```

Add dependency to BUILD.bazel:
```starlark
py_library(
    name = "my_app",
    deps = [
        "//libs/python/rmq",
        # ... other deps
    ],
)
```

## Files in This Directory

These files should be considered deprecated and will be removed in a future cleanup:
- `config.py` - Migrated to `libs/python/rmq/config.py`
- `publisher.py` - Migrated to `libs/python/rmq/publisher.py`
- `subscriber.py` - Migrated to `libs/python/rmq/subscriber.py`
- `util.py` - Migrated to `libs/python/rmq/util.py`
- `util_test.py` - Migrated to `libs/python/rmq/util_test.py`
