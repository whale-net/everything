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
    service_name: Optional[str] = None,  # Made optional - auto-detect from env
    service_version: Optional[str] = None,  # Made optional - auto-detect from env
    deployment_environment: Optional[str] = None,  # Made optional - auto-detect from env
    # Legacy parameter names for backward compatibility
    app_name: Optional[str] = None,  # DEPRECATED: use service_name
    domain: Optional[str] = None,  # Auto-detected from APP_DOMAIN
    app_type: Optional[str] = None,  # Auto-detected from APP_TYPE
    environment: Optional[str] = None,  # DEPRECATED: use deployment_environment
    version: Optional[str] = None,  # DEPRECATED: use service_version
    # Configuration options
    log_level: str = "INFO",
    enable_otlp: bool = True,  # OTLP-first
    otlp_endpoint: Optional[str] = None,
    enable_console: bool = True,  # DEPRECATED: always true now
    json_format: bool = False,  # Simple console for debug
    force_reconfigure: bool = False,
    **context_kwargs,
) -> LogContext:
    """Configure logging for the application with OTLP as the primary backend.
    
    This should be called once at application startup. It sets up:
    - OTLP export with full context as resource and log attributes (PRIMARY)
    - Optional console output for debugging
    - Global context auto-detected from environment variables
    - OpenTelemetry integration with proper semantic conventions
    
    All parameters are optional and will be auto-detected from environment variables
    if not provided. This eliminates the need to hardcode values in application code.
    
    Environment variables used (from release_app metadata + Helm charts):
    - APP_NAME: Application name (e.g., "hello-fastapi")
    - APP_VERSION: Application version (e.g., "v1.2.3")
    - APP_DOMAIN: Application domain (e.g., "demo")
    - APP_TYPE: Application type (external-api, internal-api, worker, job)
    - APP_ENV / ENVIRONMENT: Environment (dev, staging, prod)
    - GIT_COMMIT / COMMIT_SHA: Git commit SHA
    - POD_NAME, NAMESPACE, NODE_NAME: Kubernetes context (from downward API)
    - HELM_CHART_NAME, HELM_RELEASE_NAME: Helm context
    
    Args:
        service_name: Service name for OTLP (auto-detected from APP_NAME if not provided)
        service_version: Service version (auto-detected from APP_VERSION if not provided)
        deployment_environment: Environment (auto-detected from APP_ENV if not provided)
        log_level: Logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
        enable_otlp: Enable OpenTelemetry Protocol (OTLP) export (default: True)
        otlp_endpoint: OTLP collector endpoint (defaults to env or http://localhost:4317)
        json_format: Use JSON formatting for console (default: False, simple text for debug)
        force_reconfigure: Force reconfiguration even if already configured
        **context_kwargs: Additional context attributes to override auto-detected values
    
    Returns:
        LogContext: The configured global log context
        
    Example:
        >>> # Minimal usage - everything auto-detected from environment
        >>> configure_logging()
        
        >>> # Override specific values
        >>> configure_logging(
        ...     service_name="custom-name",
        ...     enable_otlp=True,
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
    
    # Start with auto-detected context from environment
    context = LogContext.from_environment()
    
    # Handle legacy parameter names (backward compatibility)
    if app_name and not service_name:
        service_name = app_name
    if environment and not deployment_environment:
        deployment_environment = environment
    if version and not service_version:
        service_version = version
    
    # Apply domain and app_type if provided
    if domain:
        context.domain = domain
    if app_type:
        context.app_type = app_type
    
    # Override with explicit parameters if provided
    if service_name:
        context.app_name = service_name
    if service_version:
        context.version = service_version
    if deployment_environment:
        context.environment = deployment_environment
    
    # Apply additional context overrides
    for key, value in context_kwargs.items():
        if hasattr(context, key):
            setattr(context, key, value)
        else:
            context.custom[key] = value
    
    # Set defaults for any still-missing values
    if not context.app_name:
        context.app_name = "unknown-app"
    if not context.environment:
        context.environment = "development"
    if not context.version:
        context.version = "latest"
    
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
    
    # Always setup console logging (can be disabled with json_format=None future enhancement)
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
        f"Logging configured for {context.app_name}",
        extra={
            "environment": context.environment,
            "domain": context.domain,
            "app_type": context.app_type,
            "version": context.version,
            "otlp_enabled": enable_otlp,
            "auto_detected": not (service_name or service_version or deployment_environment),
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
