# RabbitMQ Library (`//libs/python/rmq`)

A reusable RabbitMQ library for Python applications in the Everything monorepo. Provides connection management, publisher/subscriber patterns, and configuration utilities for RabbitMQ messaging.

## Features

- 🔌 **Connection Management** - Per-process connection pooling with SSL/TLS support
- 📤 **Publisher Pattern** - Simple message publishing to exchanges
- 📥 **Subscriber Pattern** - Threaded message consumption with automatic acknowledgment
- ⚙️ **Configuration** - Type-safe configuration with dataclasses
- 🔒 **Thread-Safe** - Safe for multi-threaded applications (Gunicorn workers)
- 🧪 **Tested** - Comprehensive unit tests

## Installation

Add to your `BUILD.bazel`:

```starlark
py_library(
    name = "my_app",
    deps = [
        "//libs/python/rmq",
        # ... other deps
    ],
)
```

## Quick Start

### 1. Initialize Connection

```python
from libs.python.rmq import init_rabbitmq, get_rabbitmq_ssl_options

# Basic connection
init_rabbitmq(
    host="localhost",
    port=5672,
    username="guest",
    password="guest",
    virtual_host="/",
)

# SSL connection
ssl_options = get_rabbitmq_ssl_options("rabbitmq.example.com")
init_rabbitmq(
    host="rabbitmq.example.com",
    port=5671,
    username="user",
    password="pass",
    ssl_enabled=True,
    ssl_options=ssl_options,
)
```

### 2. Publish Messages

```python
from libs.python.rmq import (
    get_rabbitmq_connection,
    RabbitPublisher,
    BindingConfig,
    RoutingKeyConfig,
    ExchangeRegistry,
    MessageTypeRegistry,
)

# Get connection
conn = get_rabbitmq_connection()

# Configure routing
routing_key = RoutingKeyConfig(
    entity="worker",
    identifier="123",
    type=MessageTypeRegistry.STATUS,
)

binding = BindingConfig(
    exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
    routing_keys=[routing_key],
)

# Create publisher
publisher = RabbitPublisher(conn, binding)

# Publish message
publisher.publish("Hello, RabbitMQ!")

# Cleanup
publisher.shutdown()
```

### 3. Subscribe to Messages

```python
from libs.python.rmq import (
    get_rabbitmq_connection,
    RabbitSubscriber,
    BindingConfig,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
    MessageTypeRegistry,
)

# Get connection
conn = get_rabbitmq_connection()

# Configure subscription
routing_key = RoutingKeyConfig(
    entity="worker",
    identifier=TopicWildcard.ANY,  # Subscribe to all workers
    type=MessageTypeRegistry.STATUS,
)

binding = BindingConfig(
    exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
    routing_keys=[routing_key],
)

queue = QueueConfig(
    name="my-consumer-queue",
    durable=True,
    exclusive=False,
    auto_delete=False,
)

# Create subscriber
subscriber = RabbitSubscriber(conn, binding, queue)

# Consume messages (non-blocking)
while True:
    messages = subscriber.consume()
    for message in messages:
        print(f"Received: {message}")
    time.sleep(0.1)

# Cleanup
subscriber.shutdown()
```

## Core Components

### Connection Management

```python
from libs.python.rmq import (
    init_rabbitmq,
    get_rabbitmq_connection,
    cleanup_rabbitmq_connections,
)

# Initialize (call once at app startup)
init_rabbitmq(host="localhost", port=5672, username="guest", password="guest")

# Get connection (creates per-process connection)
conn = get_rabbitmq_connection()

# Cleanup (call at app shutdown)
cleanup_rabbitmq_connections()
```

**Key Points**:
- One connection per process (safe for Gunicorn workers)
- Automatic reconnection on failure
- SSL context recreated per process (avoids fork issues)

### Configuration Classes

#### RoutingKeyConfig

Build routing keys with pattern: `entity.identifier.type[.subtype]`

```python
from libs.python.rmq import RoutingKeyConfig, TopicWildcard, MessageTypeRegistry

# Specific routing key
key = RoutingKeyConfig(
    entity="worker",
    identifier="123",
    type=MessageTypeRegistry.STATUS,
    subtype="heartbeat",  # Optional
)
# Result: "worker.123.status.heartbeat"

# Wildcard routing key
key = RoutingKeyConfig(
    entity="worker",
    identifier=TopicWildcard.ANY,  # Matches any worker
    type=MessageTypeRegistry.STATUS,
)
# Result: "worker.*.status"
```

#### QueueConfig

```python
from libs.python.rmq import QueueConfig

# Durable queue (survives broker restart)
queue = QueueConfig(
    name="my-queue",
    durable=True,
    exclusive=False,
    auto_delete=False,
)

# Temporary queue (auto-generated name, deleted on disconnect)
queue = QueueConfig(
    name="",  # Server generates name
    durable=False,
    exclusive=True,
    auto_delete=True,
)
```

#### BindingConfig

```python
from libs.python.rmq import BindingConfig, ExchangeRegistry

binding = BindingConfig(
    exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
    routing_keys=[key1, key2],  # Multiple routing keys
)
```

### Publisher

```python
from libs.python.rmq import RabbitPublisher

publisher = RabbitPublisher(
    connection=conn,
    binding_configs=binding,  # Single or list
)

publisher.publish("message content")
publisher.shutdown()
```

**Features**:
- Publishes to multiple exchanges/routing keys
- Automatic channel management
- Graceful shutdown with `__del__` fallback

### Subscriber

```python
from libs.python.rmq import RabbitSubscriber

subscriber = RabbitSubscriber(
    connection=conn,
    binding_configs=binding,  # Single or list
    queue_config=queue,
)

messages = subscriber.consume()  # Returns list of message bodies
subscriber.shutdown()
```

**Features**:
- Threaded consumption (non-blocking)
- Automatic message acknowledgment
- QoS prefetch_count=1 (fair dispatching)
- Internal message buffering

### Utilities

```python
from libs.python.rmq import add_routing_key_prefix, add_routing_key_suffix

# Add prefix
key = add_routing_key_prefix("test.route", "prefix")
# Result: "prefix.test.route"

# Add suffix
key = add_routing_key_suffix("test.route", "suffix")
# Result: "test.route.suffix"
```

## Extending Enums

Extend the registry enums for your application:

```python
from libs.python.rmq import ExchangeRegistry, MessageTypeRegistry
from enum import StrEnum

class MyExchangeRegistry(StrEnum):
    """Custom exchanges for my app"""
    MY_CUSTOM_EXCHANGE = "my_custom_exchange"
    
    # Include standard exchanges
    INTERNAL_SERVICE_EVENT = ExchangeRegistry.INTERNAL_SERVICE_EVENT
    EXTERNAL_SERVICE_EVENT = ExchangeRegistry.EXTERNAL_SERVICE_EVENT

class MyMessageTypeRegistry(StrEnum):
    """Custom message types"""
    CUSTOM_EVENT = "custom_event"
    
    # Include standard types
    STATUS = MessageTypeRegistry.STATUS
    COMMAND = MessageTypeRegistry.COMMAND
```

## Patterns

### Producer-Consumer Pattern

```python
# Producer
publisher = RabbitPublisher(conn, producer_binding)
publisher.publish(json.dumps({"task": "process_data", "id": 123}))

# Consumer
subscriber = RabbitSubscriber(conn, consumer_binding, queue)
while True:
    for message in subscriber.consume():
        data = json.loads(message)
        process_task(data)
```

### Request-Reply Pattern

```python
# Requester
reply_queue = QueueConfig(name="", exclusive=True, durable=False, auto_delete=True)
subscriber = RabbitSubscriber(conn, reply_binding, reply_queue)

publisher.publish(json.dumps({"request": "data", "reply_to": reply_queue.actual_queue_name}))

# Wait for reply
replies = subscriber.consume()

# Responder
for message in request_subscriber.consume():
    request = json.loads(message)
    reply_to = request["reply_to"]
    
    # Send reply to specific queue
    reply_routing = RoutingKeyConfig(entity="reply", identifier=reply_to, type="response")
    reply_publisher = RabbitPublisher(conn, BindingConfig(exchange, [reply_routing]))
    reply_publisher.publish(json.dumps({"result": "success"}))
```

## Testing

```bash
# Run library tests
bazel test //libs/python/rmq:util_test

# Build library
bazel build //libs/python/rmq:rmq
```

## Error Handling

The library includes comprehensive error handling:

```python
from libs.python.rmq import get_rabbitmq_connection

try:
    conn = get_rabbitmq_connection()
except RuntimeError as e:
    # Handle: init_rabbitmq() not called, or connection failure
    logger.error(f"Failed to get RabbitMQ connection: {e}")
```

## Shutdown Cleanup

Always cleanup connections on shutdown:

```python
from libs.python.rmq import cleanup_rabbitmq_connections

# In FastAPI
@app.on_event("shutdown")
async def shutdown():
    cleanup_rabbitmq_connections()

# In regular Python
import atexit
atexit.register(cleanup_rabbitmq_connections)
```

## Architecture

- **Connection Pooling**: One connection per process, stored in global state with PID key
- **Threading**: Subscriber uses daemon thread for message consumption
- **Channel Management**: Fresh channel per publisher/subscriber instance
- **SSL Handling**: SSL context recreated per process to avoid fork issues

## Migration from manman

If migrating from `manman.src.repository.rabbitmq`:

```python
# OLD
from manman.src.repository.rabbitmq.config import BindingConfig
from manman.src.repository.rabbitmq.publisher import RabbitPublisher
from manman.src.util import get_rabbitmq_connection

# NEW
from libs.python.rmq import BindingConfig, RabbitPublisher, get_rabbitmq_connection
```

See `RMQ_MIGRATION_SUMMARY.md` for full migration details.

## License

Part of the Everything monorepo.
