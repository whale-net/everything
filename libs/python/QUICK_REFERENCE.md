# Quick Reference: Unified Logging

## Setup

```python
from libs.python.log_setup import setup_logging

setup_logging(
    level=logging.INFO,
    app_name="my-app",
    app_type="external-api",
    domain="demo",
    enable_otel=False,
)
```

## Log Output Format

```
2025-10-10 05:00:00,000 - [demo/my-app/external-api/dev] module.name - INFO - Message
                          ^^^^^^^^^^^^^^^^^^^^^^^^^^^^
                          domain/app_name/app_type/env
```

## OTEL Resource Attributes

When `enable_otel=True`, logs include structured attributes:

```json
{
  "service.name": "demo-my-app",
  "service.type": "external-api",
  "service.domain": "demo",
  "deployment.environment": "dev"
}
```

## Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `APP_ENV` | Deployment environment | `dev`, `staging`, `prod` |
| `APP_NAME` | Application name | `experience-api` |
| `APP_TYPE` | Application type | `external-api` |
| `APP_DOMAIN` | Application domain | `manman` |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | OTLP logs endpoint | `http://collector:4317` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | OTLP traces endpoint | `http://collector:4317` |

## Functions

### setup_logging()
Main function to configure logging with app metadata.

### setup_server_logging()
Configure server-specific loggers (uvicorn/gunicorn).

### get_gunicorn_config()
Generate Gunicorn configuration with app metadata.

### create_formatter()
Create a standardized formatter with app metadata.

## Integration with release_app

```starlark
# BUILD.bazel
release_app(
    name = "my-app",
    domain = "demo",           # → Logging domain
    app_type = "external-api", # → Logging app_type
)
```

↓ Future: Auto-inject as environment variables

```python
# Application code reads from environment
app_name = os.getenv("APP_NAME")
app_type = os.getenv("APP_TYPE")
domain = os.getenv("APP_DOMAIN")

setup_logging(
    app_name=app_name,
    app_type=app_type,
    domain=domain,
)
```

## Files

- `log_setup.py` - Core logging module
- `log_setup_test.py` - Test suite
- `log_setup_example.py` - Basic usage example
- `integration_example.py` - Integration with metadata
- `LOGGING.md` - Complete usage guide
- `MIGRATION_GUIDE.md` - Migration from manman
- `ARCHITECTURE.md` - System architecture
- `README.md` - Overview

## Common Patterns

### Pattern 1: Basic Setup
```python
setup_logging(
    level=logging.INFO,
    app_name="my-app",
    domain="demo",
)
```

### Pattern 2: With OTEL
```python
setup_logging(
    level=logging.INFO,
    app_name="my-app",
    app_type="external-api",
    domain="demo",
    enable_otel=True,
)
```

### Pattern 3: Read from Environment
```python
setup_logging(
    level=logging.INFO,
    app_name=os.getenv("APP_NAME"),
    app_type=os.getenv("APP_TYPE"),
    domain=os.getenv("APP_DOMAIN"),
    # app_env read from APP_ENV automatically
)
```

### Pattern 4: Server Setup
```python
from libs.python.log_setup import setup_server_logging

setup_server_logging(
    app_name="my-api",
    app_type="external-api",
    domain="demo",
)
```

## Benefits

✅ **Consistent Format**: All apps use same log format  
✅ **Environment Aware**: Logs show dev/staging/prod  
✅ **OTEL Ready**: Structured attributes for distributed tracing  
✅ **Easy Filtering**: Filter by domain, app, type, or environment  
✅ **Noise Reduction**: Pre-configured to reduce third-party library noise  
✅ **Backward Compatible**: Easy migration from existing logging
