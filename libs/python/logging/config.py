"""Logging configuration and setup.

Centralized configuration for structured logging with OpenTelemetry support.
"""

import logging
import os
import sys
from typing import Optional, Dict, Any

from libs.python.logging.context import LogContext, set_context
from libs.python.logging.formatters import StructuredFormatter
from libs.python.logging.otel_handler import OTELContextHandler

try:
    from opentelemetry._logs import set_logger_provider
    from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
    from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
    from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
    from opentelemetry.sdk.resources import Resource
    
    OTEL_AVAILABLE = True
except ImportError:
    OTEL_AVAILABLE = False


# Global configuration state
_configured = False
_global_context: Optional[LogContext] = None


def configure_logging(
    app_name: str,
    domain: str,
    app_type: str = "external-api",
    environment: Optional[str] = None,
    version: Optional[str] = None,
    log_level: str = "INFO",
    enable_otlp: bool = True,  # Changed default to True - OTLP-first
    otlp_endpoint: Optional[str] = None,
    enable_console: bool = True,
    json_format: bool = False,  # Changed default to False - simple console for debug
    force_reconfigure: bool = False,
    **context_kwargs,
) -> LogContext:
    """Configure logging for the application with OTLP as the primary backend.
    
    This should be called once at application startup. It sets up:
    - OTLP export with full context as resource and log attributes (PRIMARY)
    - Optional console output for debugging
    - Global context (environment, domain, app metadata)
    - OpenTelemetry integration with proper semantic conventions
    
    All log context is sent to OTLP as structured attributes following
    OpenTelemetry semantic conventions for maximum observability.
    
    Args:
        app_name: Application name (e.g., "hello-fastapi")
        domain: Application domain (e.g., "demo", "api")
        app_type: Application type (external-api, internal-api, worker, job)
        environment: Environment (dev, staging, prod) - auto-detected if not provided
        version: Application version - auto-detected from env if not provided
        log_level: Logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
        enable_otlp: Enable OpenTelemetry Protocol (OTLP) export (default: True)
        otlp_endpoint: OTLP collector endpoint (defaults to env or http://localhost:4317)
        enable_console: Enable console logging output (default: True, for debugging)
        json_format: Use JSON formatting for console (default: False, simple text for debug)
        force_reconfigure: Force reconfiguration even if already configured
        **context_kwargs: Additional context attributes to set
    
    Returns:
        LogContext: The configured global log context
        
    Example:
        >>> configure_logging(
        ...     app_name="hello-fastapi",
        ...     domain="demo",
        ...     environment="production",
        ...     enable_otlp=True,  # Primary use case
        ... )
    """
    global _configured, _global_context
    
    # Check if already configured
    if _configured and not force_reconfigure:
        return _global_context
    
    # Clear existing handlers if reconfiguring
    root_logger = logging.getLogger()
    if force_reconfigure:
        root_logger.handlers.clear()
    
    # Auto-detect environment if not provided
    if environment is None:
        environment = os.getenv("APP_ENV") or os.getenv("ENVIRONMENT") or "development"
    
    # Auto-detect version if not provided
    if version is None:
        version = os.getenv("APP_VERSION") or os.getenv("GIT_COMMIT") or "latest"
    
    # Create global context
    context = LogContext.from_environment()
    context.app_name = app_name
    context.domain = domain
    context.app_type = app_type
    context.environment = environment
    context.version = version
    
    # Apply additional context
    for key, value in context_kwargs.items():
        if hasattr(context, key):
            setattr(context, key, value)
        else:
            context.custom[key] = value
    
    # Set as global context
    set_context(context)
    _global_context = context
    
    # Set log level
    log_level_int = getattr(logging, log_level.upper())
    root_logger.setLevel(log_level_int)
    
    # Configure OpenTelemetry if enabled and available
    if enable_otlp and OTEL_AVAILABLE:
        _setup_otlp(context, otlp_endpoint)
    elif enable_otlp and not OTEL_AVAILABLE:
        logging.warning(
            "OpenTelemetry logging requested but dependencies not available. "
            "Install with: pip install opentelemetry-api opentelemetry-sdk opentelemetry-exporter-otlp"
        )
    
    # Configure console logging if enabled
    if enable_console:
        _setup_console(context, json_format)
    
    # Reduce noise from common third-party libraries
    logging.getLogger("uvicorn.access").setLevel(logging.WARNING)
    logging.getLogger("uvicorn.error").setLevel(logging.INFO)
    logging.getLogger("gunicorn.access").setLevel(logging.WARNING)
    logging.getLogger("gunicorn.error").setLevel(logging.INFO)
    logging.getLogger("sqlalchemy").setLevel(logging.WARNING)
    logging.getLogger("sqlalchemy.engine").setLevel(logging.WARNING)
    logging.getLogger("amqpstorm").setLevel(logging.WARNING)
    
    _configured = True
    
    logging.info(
        f"Logging configured for {app_name}",
        extra={
            "environment": environment,
            "domain": domain,
            "app_type": app_type,
            "otlp_enabled": enable_otlp,
        }
    )
    
    return context


def _setup_otlp(context: LogContext, otlp_endpoint: Optional[str]) -> None:
    """Setup OpenTelemetry Protocol (OTLP) logging export with full context.
    
    Maps all LogContext fields to proper OTEL semantic conventions:
    - Resource attributes for stable service/infrastructure metadata
    - Log record attributes for request/operation context
    
    Args:
        context: Global log context
        otlp_endpoint: OTLP collector endpoint
    """
    # Create resource attributes from context (stable service metadata)
    # Following OTEL semantic conventions: https://opentelemetry.io/docs/specs/semconv/
    resource_attrs = {
        # Service identification (REQUIRED)
        "service.name": context.app_name,
        "service.namespace": context.domain,
        "service.version": context.version or "unknown",
        
        # Deployment metadata
        "deployment.environment": context.environment or "unknown",
    }
    
    # Add commit/build metadata
    if context.commit_sha:
        resource_attrs["service.instance.id"] = context.commit_sha[:8]
        resource_attrs["vcs.commit.id"] = context.commit_sha
    
    # Add custom service type (our domain-specific attribute)
    if context.app_type:
        resource_attrs["service.type"] = context.app_type
    
    # Kubernetes resource attributes (semantic conventions)
    if context.pod_name:
        resource_attrs["k8s.pod.name"] = context.pod_name
    if context.namespace:
        resource_attrs["k8s.namespace.name"] = context.namespace
    if context.node_name:
        resource_attrs["k8s.node.name"] = context.node_name
    if context.container_name:
        resource_attrs["k8s.container.name"] = context.container_name
    
    # Host attributes
    if context.hostname:
        resource_attrs["host.name"] = context.hostname
    if context.platform:
        resource_attrs["host.arch"] = context.platform  # e.g., "linux/arm64"
    if context.architecture:
        resource_attrs["host.cpu.family"] = context.architecture  # e.g., "arm64"
    
    # Helm/deployment attributes (custom)
    if context.chart_name:
        resource_attrs["helm.chart.name"] = context.chart_name
    if context.release_name:
        resource_attrs["helm.release.name"] = context.release_name
    
    # Build attributes (custom)
    if context.bazel_target:
        resource_attrs["build.target"] = context.bazel_target
    
    # Create logger provider with resource
    resource = Resource.create(resource_attrs)
    logger_provider = LoggerProvider(resource=resource)
    set_logger_provider(logger_provider)
    
    # Configure OTLP endpoint
    endpoint = (
        otlp_endpoint
        or os.getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
        or os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
        or "http://localhost:4317"
    )
    
    # Add OTLP exporter with batching for efficiency
    otlp_exporter = OTLPLogExporter(endpoint=endpoint, insecure=True)
    logger_provider.add_log_record_processor(
        BatchLogRecordProcessor(otlp_exporter)
    )
    
    # Add handler to root logger with context-aware handler
    handler = OTELContextHandler(level=logging.NOTSET, logger_provider=logger_provider)
    logging.getLogger().addHandler(handler)
    
    logging.debug(f"OTLP logging enabled: {endpoint}")
    logging.debug(f"OTLP resource attributes: {resource_attrs}")


def _setup_console(context: LogContext, json_format: bool) -> None:
    """Setup console logging output.
    
    Args:
        context: Global log context
        json_format: Whether to use JSON formatting
    """
    handler = logging.StreamHandler(sys.stdout)
    
    if json_format:
        formatter = StructuredFormatter(context)
    else:
        # Simple text format for development
        formatter = logging.Formatter(
            f"%(asctime)s - [{context.app_name}] %(name)s - %(levelname)s - %(message)s"
        )
    
    handler.setFormatter(formatter)
    logging.getLogger().addHandler(handler)
    
    logging.debug(f"Console logging enabled (json={json_format})")


def get_global_context() -> Optional[LogContext]:
    """Get the global log context set during configuration.
    
    Returns:
        Global LogContext or None if not configured
    """
    return _global_context


def is_configured() -> bool:
    """Check if logging has been configured.
    
    Returns:
        True if configure_logging has been called
    """
    return _configured
