# Retry Library (`//libs/python/retry`)

A general-purpose retry library with exponential backoff for handling transient failures in distributed systems.

## Features

- 🔄 Exponential backoff with configurable base and max delay
- 🎲 Optional jitter to avoid thundering herd
- 🎯 Flexible exception filtering
- 📊 Retry callbacks for monitoring/logging
- 🔌 Both sync and async support
- 🌐 Built-in HTTP error detection (502, 503, 504, timeouts, connection errors)
- 🐰 Built-in RabbitMQ error detection

## Quick Start

### Basic Usage

```python
from libs.python.retry import retry, RetryConfig

# Use defaults (3 attempts, 1s initial delay, exponential backoff)
@retry()
def fetch_data():
    return requests.get("http://api.example.com/data")

# Custom configuration
@retry(RetryConfig(
    max_attempts=5,
    initial_delay=2.0,
    max_delay=30.0,
))
def important_operation():
    # Your code here
    pass
```

### Async Support

```python
from libs.python.retry import retry_async, RetryConfig

@retry_async(RetryConfig(max_attempts=5))
async def fetch_data_async():
    async with httpx.AsyncClient() as client:
        return await client.get("http://api.example.com/data")
```

### HTTP-Specific Retries

```python
from libs.python.retry import retry, RetryConfig, is_transient_http_error
import requests

@retry(RetryConfig(
    max_attempts=5,
    initial_delay=1.0,
    max_delay=60.0,
    exceptions=(requests.exceptions.RequestException,),
    exception_filter=is_transient_http_error,
))
def api_call():
    response = requests.get("http://api.example.com/data")
    response.raise_for_status()
    return response.json()
```

This will retry on:
- Connection errors
- Timeouts
- 502 Bad Gateway
- 503 Service Unavailable
- 504 Gateway Timeout

### RabbitMQ Connection Retries

```python
from libs.python.retry import retry, RetryConfig, is_transient_rmq_error
import amqpstorm

@retry(RetryConfig(
    max_attempts=10,
    initial_delay=1.0,
    max_delay=30.0,
    exception_filter=is_transient_rmq_error,
))
def connect_rabbitmq():
    return amqpstorm.Connection(
        hostname="rabbitmq.example.com",
        username="user",
        password="pass",
    )
```

### Custom Exception Filtering

```python
from libs.python.retry import retry, RetryConfig

def is_retryable(exc: Exception) -> bool:
    # Custom logic to determine if error is retryable
    if isinstance(exc, ValueError):
        return "temporary" in str(exc).lower()
    return False

@retry(RetryConfig(
    exceptions=(ValueError, ConnectionError),
    exception_filter=is_retryable,
))
def custom_operation():
    # Your code here
    pass
```

### Monitoring with Callbacks

```python
from libs.python.retry import retry, RetryConfig

def on_retry_callback(exc: Exception, attempt: int, delay: float):
    print(f"Retry attempt {attempt} after {delay}s due to {type(exc).__name__}")
    # Send to metrics, logging, etc.

@retry(RetryConfig(
    max_attempts=5,
    on_retry=on_retry_callback,
))
def monitored_operation():
    # Your code here
    pass
```

## Configuration Reference

### RetryConfig Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `max_attempts` | `int` | `3` | Maximum number of attempts (including initial) |
| `initial_delay` | `float` | `1.0` | Initial delay in seconds before first retry |
| `max_delay` | `float` | `60.0` | Maximum delay between retries |
| `exponential_base` | `float` | `2.0` | Base for exponential backoff |
| `jitter` | `bool` | `True` | Whether to add random jitter |
| `jitter_factor` | `float` | `0.1` | Jitter amount (±10% by default) |
| `exceptions` | `Tuple[Type[Exception], ...]` | `(Exception,)` | Exception types to retry |
| `exception_filter` | `Callable[[Exception], bool]` | `None` | Custom filter function |
| `on_retry` | `Callable[[Exception, int, float], None]` | `None` | Callback before each retry |

### Backoff Calculation

Delay is calculated as:
```
delay = min(initial_delay * (exponential_base ** attempt), max_delay)
```

With jitter enabled:
```
jitter_amount = delay * jitter_factor
delay += random.uniform(-jitter_amount, jitter_amount)
```

Example with defaults (base=2.0, initial=1.0, max=60.0):
- Attempt 1: ~1.0s
- Attempt 2: ~2.0s
- Attempt 3: ~4.0s
- Attempt 4: ~8.0s
- Attempt 5: ~16.0s
- etc. (capped at 60s)

## Built-in Error Detectors

### `is_transient_http_error(exception)`

Detects transient HTTP errors that should be retried:
- Connection errors (DNS, network, etc.)
- Timeouts (connect, read)
- HTTP status codes: 502, 503, 504

Supports:
- `requests` library
- `httpx` library
- `urllib3` exceptions

### `is_transient_rmq_error(exception)`

Detects transient RabbitMQ errors:
- Connection errors
- Channel errors
- Stream/broker disconnects

Supports:
- `amqpstorm` library
- `pika` library

## Integration Examples

### Worker Service with API Retry

```python
from libs.python.retry import retry, RetryConfig, is_transient_http_error
import requests

class WorkerService:
    @retry(RetryConfig(
        max_attempts=5,
        initial_delay=2.0,
        exception_filter=is_transient_http_error,
    ))
    def create_worker(self):
        response = requests.post(
            f"{self.api_url}/worker/create",
            json={},
        )
        response.raise_for_status()
        return response.json()
```

### RabbitMQ Connection with Retry

```python
from libs.python.retry import retry, RetryConfig, is_transient_rmq_error
import amqpstorm

@retry(RetryConfig(
    max_attempts=10,
    initial_delay=1.0,
    max_delay=30.0,
    exception_filter=is_transient_rmq_error,
))
def get_rabbitmq_connection():
    connection = amqpstorm.Connection(
        hostname=params["host"],
        port=params["port"],
        username=params["username"],
        password=params["password"],
    )
    return connection
```

## Testing

```bash
bazel test //libs/python/retry:test_retry
```

## Design Principles

1. **Transient Failures Only**: Retry logic should only apply to temporary failures, not permanent errors
2. **Exponential Backoff**: Prevent overwhelming services during recovery
3. **Jitter**: Avoid synchronized retry storms across multiple clients
4. **Configurable**: Support different retry strategies for different use cases
5. **Observable**: Provide callbacks for monitoring and debugging

## When to Use Retries

✅ **Good candidates:**
- Network requests to external services
- Database connection establishment
- Message queue connections
- Distributed lock acquisition
- Cloud API calls

❌ **Poor candidates:**
- User input validation errors
- Business logic errors
- Authentication/authorization failures (usually permanent)
- Resource not found errors (404)
- Operations with side effects that aren't idempotent

## Best Practices

1. **Set appropriate max_attempts**: Too few = unnecessary failures, too many = long delays
2. **Use exception filters**: Only retry truly transient errors
3. **Cap max_delay**: Prevent excessively long waits
4. **Enable jitter**: Helps with distributed system stability
5. **Monitor retries**: Use `on_retry` callback to track retry rates
6. **Consider idempotency**: Ensure retried operations are safe to repeat
7. **Set timeouts**: Combine retries with operation timeouts

## See Also

- [Circuit Breaker Pattern](https://martinfowler.com/bliki/CircuitBreaker.html)
- [Exponential Backoff Algorithm](https://en.wikipedia.org/wiki/Exponential_backoff)
- [AWS Error Retries and Exponential Backoff](https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/)
