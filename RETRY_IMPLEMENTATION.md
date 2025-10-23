# Retry Implementation Summary

## Overview

Added comprehensive retry functionality to handle transient failures in the worker service, addressing issues with gateway 502 errors and RabbitMQ connection drops.

## Changes Made

### 1. New Retry Library (`//libs/python/retry`)

Created a general-purpose retry library with exponential backoff and jitter:

**Files:**
- `libs/python/retry/__init__.py` - Public API exports
- `libs/python/retry/retry.py` - Core retry implementation
- `libs/python/retry/test_retry.py` - Comprehensive test suite
- `libs/python/retry/BUILD.bazel` - Bazel build configuration
- `libs/python/retry/README.md` - Complete documentation

**Features:**
- ✅ Exponential backoff with configurable base and max delay
- ✅ Optional jitter to prevent thundering herd
- ✅ Flexible exception filtering
- ✅ Both sync (`@retry`) and async (`@retry_async`) decorators
- ✅ Built-in HTTP error detection (`is_transient_http_error`)
- ✅ Built-in RabbitMQ error detection (`is_transient_rmq_error`)
- ✅ Retry callbacks for monitoring/logging

**Default Configuration:**
```python
RetryConfig(
    max_attempts=3,
    initial_delay=1.0,
    max_delay=60.0,
    exponential_base=2.0,
    jitter=True,
    jitter_factor=0.1,
)
```

### 2. RabbitMQ Connection Retry (`//libs/python/rmq`)

**Modified:** `libs/python/rmq/connection.py`

Added automatic retry to `get_rabbitmq_connection()`:
- Refactored connection creation into `_create_connection()` function
- Applied retry decorator with 10 attempts, exponential backoff (1s → 30s max)
- Only retries on transient RMQ errors (connection failures, channel errors)
- Logs retry attempts with detailed context

**Retry Configuration:**
```python
RetryConfig(
    max_attempts=10,
    initial_delay=1.0,
    max_delay=30.0,
    exponential_base=2.0,
    exception_filter=is_transient_rmq_error,
)
```

**Updated BUILD:** Added dependency on `//libs/python/retry`

### 3. Worker DAL Client Retry (`//manman/clients`)

**Modified:** `manman/clients/worker_dal_client.py`

Added retry decorators to all API methods:
- `create_worker()`
- `shutdown_worker()`
- `heartbeat_worker()`
- `shutdown_other_workers()`
- `create_game_server_instance()`
- `shutdown_game_server_instance()`
- `get_game_server_instance()`
- `heartbeat_game_server_instance()`
- `get_game_server()`
- `get_game_server_config()`

**Retry Configuration:**
```python
RetryConfig(
    max_attempts=5,
    initial_delay=1.0,
    max_delay=30.0,
    exponential_base=2.0,
    exception_filter=is_transient_http_error,
)
```

**Retryable HTTP Errors:**
- Connection errors (network, DNS)
- Timeouts (connect, read)
- 502 Bad Gateway
- 503 Service Unavailable
- 504 Gateway Timeout

**Updated BUILD:** Added dependency on `//libs/python/retry`

## Testing

All tests pass:
```bash
bazel test //libs/python/retry:test_retry  # PASSED
bazel build //libs/python/rmq:rmq          # SUCCESS
bazel build //manman/clients:clients       # SUCCESS
bazel build //manman/src/worker:worker     # SUCCESS
```

## Retry Behavior Examples

### RabbitMQ Connection Failure

Before:
```
ERROR: Failed to create RabbitMQ connection: AMQPConnectionError
[Service crashes]
```

After:
```
WARNING: Attempt 1/10 failed with AMQPConnectionError. Retrying in 1.0s...
WARNING: Attempt 2/10 failed with AMQPConnectionError. Retrying in 2.0s...
INFO: RabbitMQ connection established for process 12345
```

### API Gateway 502 Error

Before:
```
ConnectionError: 502 Bad Gateway
[Worker fails to register]
```

After:
```
WARNING: Attempt 1/5 failed with HTTPError: 502. Retrying in 1.0s...
WARNING: Attempt 2/5 failed with HTTPError: 502. Retrying in 2.0s...
INFO: Worker created successfully
```

## Exponential Backoff Schedule

With default settings (base=2.0, initial=1.0):

| Attempt | Delay (approx) |
|---------|----------------|
| 1       | 1.0s ± 10%    |
| 2       | 2.0s ± 10%    |
| 3       | 4.0s ± 10%    |
| 4       | 8.0s ± 10%    |
| 5       | 16.0s ± 10%   |
| 6+      | 30.0s ± 10% (capped) |

Jitter prevents synchronized retry storms across multiple workers.

## Monitoring

The retry library logs all retry attempts at WARNING level:
```
WARNING: Attempt 2/5 failed for create_worker with HTTPError: 502 Bad Gateway. Retrying in 2.1s...
```

Future enhancement: Add custom `on_retry` callback for metrics collection:
```python
def send_to_metrics(exc: Exception, attempt: int, delay: float):
    metrics.increment("worker.api.retry", tags=[f"attempt:{attempt}"])

@retry(RetryConfig(on_retry=send_to_metrics))
def api_call():
    ...
```

## Design Principles

1. **Transient failures only** - Only retry temporary errors, not permanent failures
2. **Exponential backoff** - Prevent overwhelming recovering services
3. **Jitter** - Avoid synchronized retry storms
4. **Configurable** - Different strategies for different use cases
5. **Observable** - Log all retries for debugging

## Future Enhancements

Potential improvements:
- Circuit breaker pattern (fail fast after repeated failures)
- Retry budget (limit total retry time across all calls)
- Adaptive retry (adjust based on success rate)
- Metrics integration (Prometheus/OpenTelemetry)
- Per-operation timeout configuration

## Documentation

See `libs/python/retry/README.md` for:
- Complete API reference
- Usage examples
- Configuration guide
- Best practices
- Integration patterns
