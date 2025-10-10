# Unified Logging Library

The `//libs/python:log_setup` module provides a unified logging configuration that integrates release app properties (app_type, app_name, domain) with deployment properties (environment via APP_ENV).

## Features

- **App Metadata Integration**: Automatically includes app_name, app_type, and domain in log messages
- **Environment Awareness**: Reads APP_ENV environment variable for deployment context
- **OTEL Support**: Optional OpenTelemetry integration for structured logging and tracing
- **Consistent Formatting**: Standardized log format across all applications
- **Third-party Library Noise Reduction**: Pre-configured to reduce noise from uvicorn, SQLAlchemy, etc.

## Basic Usage

```python
from libs.python.log_setup import setup_logging

# Setup logging with app metadata
setup_logging(
    level=logging.INFO,
    app_name="experience-api",
    app_type="external-api",
    domain="manman",
    # app_env is read from APP_ENV environment variable if not provided
    enable_otel=False,  # Enable for production
    enable_console=True,
)
```

## Usage with Release Metadata

The logging library is designed to work seamlessly with the `release_app` metadata:

```python
# In your app's initialization
import logging
import os
from libs.python.log_setup import setup_logging

# Read from environment or config
APP_NAME = os.getenv("APP_NAME", "my-app")
APP_TYPE = os.getenv("APP_TYPE", "external-api")
DOMAIN = os.getenv("DOMAIN", "demo")

setup_logging(
    level=logging.INFO,
    app_name=APP_NAME,
    app_type=APP_TYPE,
    domain=DOMAIN,
    enable_otel=True,
    otel_endpoint=os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
)

logger = logging.getLogger(__name__)
logger.info("Application started")
```

## Log Format

The standardized formatter produces logs in the format:

```
2025-10-10 05:00:00,000 - [domain/app-name/app-type/env] module.name - INFO - Log message
```

Example:
```
2025-10-10 05:00:00,123 - [manman/experience-api/external-api/dev] manman.src.host.api - INFO - Processing request
```

## Server Logging Setup

For web servers using uvicorn/gunicorn:

```python
from libs.python.log_setup import setup_server_logging, get_gunicorn_config

# Setup server-specific loggers
setup_server_logging(
    app_name="experience-api",
    app_type="external-api",
    domain="manman",
    app_env="dev",
)

# Get Gunicorn config
gunicorn_config = get_gunicorn_config(
    app_name="experience-api",
    app_type="external-api",
    domain="manman",
    app_env="dev",
    port=8000,
    workers=2,
)
```

## OTEL Integration

When OTEL is enabled, the library automatically:

1. Sets up structured logging with resource attributes:
   - `service.name`: Formatted as `{domain}-{app_name}`
   - `service.type`: The app_type value
   - `service.domain`: The domain value
   - `deployment.environment`: The app_env value
   - `service.instance.id`: The hostname

2. Configures distributed tracing with the same resource attributes

3. Exports to OTLP endpoints configured via environment variables:
   - `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`
   - `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`
   - `OTEL_EXPORTER_OTLP_ENDPOINT` (fallback)

## Environment Variables

The library reads the following environment variables:

- `APP_ENV`: Deployment environment (dev, staging, prod)
- `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`: OTLP logs endpoint
- `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`: OTLP traces endpoint  
- `OTEL_EXPORTER_OTLP_ENDPOINT`: Fallback OTLP endpoint

## Migration from Existing Logging

If you're migrating from manman's `logging_config.py`:

### Before:
```python
from manman.src.logging_config import setup_logging

setup_logging(
    level=logging.INFO,
    microservice_name="experience-api",
    app_env="dev",
    enable_otel=True,
)
```

### After:
```python
from libs.python.log_setup import setup_logging

setup_logging(
    level=logging.INFO,
    app_name="experience-api",
    app_type="external-api",
    domain="manman",
    # app_env read from APP_ENV automatically
    enable_otel=True,
)
```

The main differences:
- `microservice_name` → `app_name`
- Added `app_type` parameter (from release metadata)
- Added `domain` parameter (from release metadata)
- `app_env` now defaults to reading from `APP_ENV` environment variable

## Dependency

Add to your BUILD.bazel:

```starlark
py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = [
        "//libs/python",  # Includes log_setup module
        # ... other deps
    ],
)
```

The log_setup module is included as part of the `//libs/python` library.
