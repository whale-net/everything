"""Consolidated structured logging with rich context.

This library provides a standardized approach to logging across all applications
in the monorepo with automatic context injection and OpenTelemetry integration.

Key features:
- Automatic injection of standard attributes (environment, domain, app_name, etc.)
- OpenTelemetry trace/span correlation
- Kubernetes context (pod, node, namespace)
- Request correlation (request_id, user_id, etc.)
- Consistent JSON formatting for log aggregation

Example:
    ```python
    from libs.python.logging import get_logger, configure_logging
    
    # Configure once at app startup
    configure_logging(
        app_name="my-app",
        domain="api",
        environment="production",
        enable_otlp=True,
    )
    
    # Use throughout your app
    logger = get_logger(__name__)
    logger.info("Processing request", extra={
        "request_id": "abc-123",
        "user_id": "user-456",
    })
    ```
"""

from libs.python.logging.config import configure_logging
from libs.python.logging.factory import get_logger
from libs.python.logging.context import (
    LogContext,
    set_context,
    get_context,
    clear_context,
    update_context,
)

__all__ = [
    "configure_logging",
    "get_logger",
    "LogContext",
    "set_context",
    "get_context",
    "clear_context",
    "update_context",
]
