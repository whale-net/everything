# RabbitMQ Connection Wrapper

## Overview

The `connection_wrapper.py` module provides a resilient wrapper around AMQPStorm connections to handle transient connection failures gracefully.

## Problem Solved

Workers were encountering fatal AMQP connection errors like:
```
Connection dead, no heartbeat or data received in >= 60s
```

This would cause immediate service shutdown, even for transient network issues.

## Solution

### ResilientConnection

A lightweight wrapper that:
- Validates connection health before operations
- Automatically reconnects on stale/dead connections
- Provides retry logic for transient failures with exponential backoff
- Invalidates failed connections to force reconnection on retry

### Enhanced RabbitPublisher

The `RabbitPublisher` class now includes:
- Automatic channel health checks with `_ensure_channel()`
- Retry logic on publish operations (3 attempts, exponential backoff)
- Graceful handling of connection timeouts

## Usage

### Basic Usage

The connection wrapper is primarily used internally, but can be used directly:

```python
from libs.python.rmq import ResilientConnection, get_rabbitmq_connection

# Create a factory function
def create_connection():
    return get_rabbitmq_connection()

# Wrap it for resilience
resilient_conn = ResilientConnection(create_connection)

# Use it - automatically reconnects on failure
channel = resilient_conn.channel()
```

### Publisher Usage

The publisher now automatically retries on transient failures:

```python
from libs.python.rmq import RabbitPublisher, BindingConfig

# Create publisher (same as before)
publisher = RabbitPublisher(
    connection=connection,
    binding_configs=BindingConfig(exchange="my.exchange", routing_keys=["key"])
)

# Publish - now with automatic retry
publisher.publish("my message")  # Retries up to 3 times on connection errors
```

## Configuration

### Retry Behavior

Default retry configuration in `RabbitPublisher`:
- **Max attempts**: 3
- **Initial delay**: 0.5 seconds
- **Max delay**: 5 seconds
- **Exponential base**: 2.0 (delays: 0.5s, 1.0s, 2.0s on first 3 attempts)

### Transient Errors

The following errors trigger retry:
- `AMQPConnectionError` - Connection lost/timeout
- `AMQPChannelError` - Channel-level errors
- Generic `ConnectionError` and `OSError`

## Testing

Comprehensive test coverage:

```bash
# Run all RMQ tests
pytest libs/python/rmq/ -v

# Run specific tests
pytest libs/python/rmq/connection_wrapper_test.py -v
pytest libs/python/rmq/publisher_test.py -v
```

## Architecture

### Connection Lifecycle

1. **Lazy initialization**: Connection created on first use
2. **Health checks**: Before each operation, validates connection is open
3. **Automatic reconnection**: Closed connections trigger new connection
4. **Retry on failure**: Operations retry with exponential backoff
5. **Connection invalidation**: Failed connections are cleared for clean retry

### Retry Flow

```
Operation requested
    ↓
Check connection health
    ↓
Reconnect if needed
    ↓
Try operation
    ↓
On failure:
  - Invalidate connection
  - Wait (exponential backoff)
  - Retry (up to max attempts)
```

## Benefits

1. **Resilience**: Services survive transient network issues
2. **Automatic recovery**: No manual intervention needed
3. **Minimal changes**: Existing code works without modification
4. **Configurable**: Retry behavior can be tuned per use case
5. **Observable**: Comprehensive logging of retry attempts

## Limitations

1. **Not for persistent failures**: Max 3 retries, then fails
2. **Blocking retries**: Sleeps during backoff (no async support yet)
3. **Channel recreation**: Each retry may create new channels
4. **No circuit breaker**: Doesn't implement circuit breaker pattern

## Future Enhancements

Potential improvements:
- Async support for non-blocking retries
- Circuit breaker pattern for persistent failures
- Metrics/monitoring integration
- Connection pooling support
- Configurable retry policies per publisher
