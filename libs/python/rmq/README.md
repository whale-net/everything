# libs/python/rmq - Generic RabbitMQ Library

A generic, reusable RabbitMQ consumer/producer library extracted from manman.

## Overview

This library provides generic RabbitMQ patterns for publishing and subscribing to messages. It's designed to be framework-agnostic and can be used in any Python project that needs RabbitMQ functionality.

## Components

### Configuration Classes (`config.py`)

#### `TopicWildcard`
Enum for RabbitMQ topic wildcards:
- `ANY = "*"` - Matches exactly one word
- `ALL = "#"` - Matches zero or more words

#### `RoutingKeyConfig`
Builds routing keys in the format: `entity.identifier.type[.subtype]`

```python
from libs.python.rmq import RoutingKeyConfig, TopicWildcard

# Basic routing key
rk = RoutingKeyConfig(entity="worker", identifier="123", type="status")
print(rk.build_key())  # "worker.123.status"

# With subtype
rk = RoutingKeyConfig(entity="worker", identifier="123", type="status", subtype="active")
print(rk.build_key())  # "worker.123.status.active"

# With wildcards
rk = RoutingKeyConfig(entity="worker", identifier=TopicWildcard.ANY, type=TopicWildcard.ALL)
print(rk.build_key())  # "worker.*.#"
```

#### `QueueConfig`
Configuration for RabbitMQ queues:

```python
from libs.python.rmq import QueueConfig

qc = QueueConfig(
    name="my-queue",
    durable=True,      # Queue survives broker restart
    exclusive=False,   # Queue can be accessed by multiple connections
    auto_delete=False  # Queue remains after consumer disconnects
)
```

#### `BindingConfig`
Binds queues to exchanges with routing keys:

```python
from libs.python.rmq import BindingConfig

bc = BindingConfig(
    exchange="my-exchange",
    routing_keys=[rk1, rk2, "direct.routing.key"]
)
```

### Publisher (`publisher.py`)

#### `MessagePublisherInterface`
Abstract interface for message publishers.

#### `RabbitPublisher`
Publishes messages to RabbitMQ exchanges:

```python
from libs.python.rmq import RabbitPublisher, BindingConfig, RoutingKeyConfig
from amqpstorm import Connection

connection = Connection("localhost", "guest", "guest")

binding = BindingConfig(
    exchange="my-exchange",
    routing_keys=[
        RoutingKeyConfig(entity="worker", identifier="123", type="status")
    ]
)

publisher = RabbitPublisher(connection, binding)
publisher.publish("Hello, RabbitMQ!")
publisher.shutdown()
```

### Subscriber (`subscriber.py`)

#### `MessageSubscriberInterface`
Abstract interface for message subscribers.

#### `RabbitSubscriber`
Subscribes to RabbitMQ exchanges and consumes messages:

```python
from libs.python.rmq import RabbitSubscriber, BindingConfig, QueueConfig, RoutingKeyConfig
from amqpstorm import Connection

connection = Connection("localhost", "guest", "guest")

binding = BindingConfig(
    exchange="my-exchange",
    routing_keys=[
        RoutingKeyConfig(entity="worker", identifier="*", type="#")
    ]
)

queue = QueueConfig(
    name="my-consumer-queue",
    durable=True,
    exclusive=False,
    auto_delete=True
)

subscriber = RabbitSubscriber(connection, binding, queue)

# Consume messages (non-blocking)
messages = subscriber.consume()
for msg in messages:
    print(f"Received: {msg}")

subscriber.shutdown()
```

### Utilities (`util.py`)

#### `add_routing_key_prefix(routing_key, prefix)`
Adds a prefix to a routing key:

```python
from libs.python.rmq.util import add_routing_key_prefix

key = add_routing_key_prefix("test.route", "prefix")
print(key)  # "prefix.test.route"
```

#### `add_routing_key_suffix(routing_key, suffix)`
Adds a suffix to a routing key:

```python
from libs.python.rmq.util import add_routing_key_suffix

key = add_routing_key_suffix("test.route", "suffix")
print(key)  # "test.route.suffix"
```

## Usage in Bazel

Add to your `BUILD.bazel`:

```starlark
py_library(
    name = "my_app",
    srcs = ["main.py"],
    deps = [
        "//libs/python/rmq",
        "@pypi//:amqpstorm",
    ],
)
```

## Lazy Imports

The library uses lazy imports to avoid requiring `amqpstorm` at import time. This means you can import config classes and utilities without having `amqpstorm` installed:

```python
# These work without amqpstorm
from libs.python.rmq import RoutingKeyConfig, QueueConfig, BindingConfig
from libs.python.rmq.util import add_routing_key_prefix

# These require amqpstorm
from libs.python.rmq import RabbitPublisher, RabbitSubscriber
```

## Dependencies

- `amqpstorm`: RabbitMQ client library
- `pydantic`: For data validation (optional, for future enhancements)

## Extension

To create domain-specific versions of this library (like manman does), create your own config classes that define specific types:

```python
from dataclasses import dataclass
from enum import StrEnum
from typing import Union
from libs.python.rmq import RoutingKeyConfig as GenericRoutingKeyConfig, TopicWildcard

class MyEntityType(StrEnum):
    WORKER = "worker"
    SERVER = "server"

class MyMessageType(StrEnum):
    STATUS = "status"
    COMMAND = "command"

@dataclass
class RoutingKeyConfig:
    entity: Union[MyEntityType, TopicWildcard]
    identifier: Union[str, TopicWildcard]
    type: Union[MyMessageType, TopicWildcard]
    subtype: Union[str, TopicWildcard, None] = None

    def build_key(self) -> str:
        entity_str = str(self.entity)
        identifier_str = str(self.identifier)
        type_str = str(self.type)
        if self.subtype is None:
            subtype_str = ""
        else:
            subtype_str = f".{self.subtype}"
        return f"{entity_str}.{identifier_str}.{type_str}{subtype_str}"

    def __str__(self) -> str:
        return self.build_key()
```

## Testing

Run tests with Bazel:

```bash
bazel test //libs/python/rmq:rmq_util_test
```

## Migration from manman

If you're migrating from manman's RabbitMQ module:

1. Update imports from `manman.src.repository.rabbitmq` to `libs.python.rmq`
2. If you use ManMan-specific types (EntityRegistry, ExchangeRegistry, MessageTypeRegistry), continue using `manman.src.repository.rabbitmq`
3. No code changes needed - the API is identical

## License

Same as the parent repository.
