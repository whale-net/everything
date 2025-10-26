"""Consolidated structured logging with rich context.

This library provides a standardized approach to logging across all applications
in the monorepo with automatic context injection and OpenTelemetry integration.

Key features:
- Automatic injection of standard attributes (environment, domain, app_name, etc.)
- OpenTelemetry trace/span correlation
- OpenTelemetry metrics collection and export
- Kubernetes context (pod, node, namespace)
- Request correlation (request_id, user_id, etc.)
- Consistent JSON formatting for log aggregation

Example:
    ```python
    from libs.python.logging import get_logger, configure_logging, configure_metrics
    
    # Configure once at app startup
    configure_logging(
        app_name="my-app",
        domain="api",
        environment="production",
        enable_otlp=True,
    )
    
    # Optionally enable metrics
    configure_metrics()
    
    # Use throughout your app
    logger = get_logger(__name__)
    logger.info("Processing request", extra={
        "request_id": "abc-123",
        "user_id": "user-456",
    })
    ```
"""

from libs.python.logging.config import configure_logging, is_configured
from libs.python.logging.factory import get_logger
from libs.python.logging.metrics import configure_metrics, is_metrics_configured
from libs.python.logging.context import (
    LogContext,
    set_context,
    get_context,
    clear_context,
    update_context,
)

__all__ = [
    "configure_logging",
    "is_configured",
    "configure_metrics",
    "is_metrics_configured",
    "get_logger",
    "LogContext",
    "set_context",
    "get_context",
    "clear_context",
    "update_context",
]
