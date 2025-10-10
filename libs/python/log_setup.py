"""
Unified logging configuration for Everything monorepo applications.

This module provides consistent logging setup across all services,
integrating with release app properties (app_type, app_name, domain)
and deployment properties (environment via APP_ENV).
"""

import logging
import os
import sys
from typing import Optional

try:
    from opentelemetry._logs import set_logger_provider
    from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
    from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
    from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
    from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
    from opentelemetry.sdk.resources import Resource
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import BatchSpanProcessor
    from opentelemetry.trace import set_tracer_provider

    OTEL_AVAILABLE = True
except ImportError:
    OTEL_AVAILABLE = False


def setup_logging(
    level: int = logging.INFO,
    app_name: Optional[str] = None,
    app_type: Optional[str] = None,
    domain: Optional[str] = None,
    app_env: Optional[str] = None,
    force_setup: bool = False,
    enable_otel: bool = False,
    enable_console: bool = True,
    otel_endpoint: Optional[str] = None,
) -> None:
    """
    Setup unified logging configuration for monorepo applications.

    This function integrates release app properties (app_name, app_type, domain)
    with deployment properties (app_env from APP_ENV environment variable).

    Args:
        level: Logging level (default: INFO)
        app_name: Application name from release metadata
        app_type: Application type (e.g., 'external-api', 'internal-api', 'worker', 'job')
        domain: Domain/category for the app (e.g., 'demo', 'manman')
        app_env: Application environment (e.g., 'dev', 'staging', 'prod')
                 If not provided, reads from APP_ENV environment variable
        force_setup: Whether to force reconfiguration even if already setup
        enable_otel: Whether to enable OTEL logging (default: False)
        enable_console: Whether to enable console logging (default: True)
        otel_endpoint: OTEL collector endpoint (defaults to env var or localhost)
    """
    # Check if logging has already been configured
    root_logger = logging.getLogger()
    if root_logger.handlers and not force_setup:
        # Logging already configured, just ensure our level is set
        root_logger.setLevel(level)
        return

    # Clear any existing handlers if we're forcing setup
    if force_setup:
        root_logger.handlers.clear()

    # Get environment from APP_ENV if not provided
    if app_env is None:
        app_env = os.getenv("APP_ENV")

    # Setup OTEL logging and tracing if available and enabled
    if enable_otel and OTEL_AVAILABLE:
        _setup_otel_logging(
            app_name=app_name,
            app_type=app_type,
            domain=domain,
            app_env=app_env,
            otel_endpoint=otel_endpoint,
        )
        _setup_otel_tracing(
            app_name=app_name,
            app_type=app_type,
            domain=domain,
            app_env=app_env,
            otel_endpoint=otel_endpoint,
        )

    # Setup console logging if enabled
    if enable_console:
        _setup_console_logging(
            app_name=app_name,
            app_type=app_type,
            domain=domain,
            app_env=app_env,
        )

    # Configure root logger
    root_logger.setLevel(level)

    # Set specific loggers to appropriate levels
    # Reduce noise from common third-party libraries
    logging.getLogger("uvicorn.access").setLevel(logging.WARNING)
    logging.getLogger("amqpstorm").setLevel(logging.WARNING)

    # SQLAlchemy has multiple loggers - set them all to WARNING to reduce noise
    logging.getLogger("sqlalchemy").setLevel(logging.WARNING)
    logging.getLogger("sqlalchemy.engine").setLevel(logging.WARNING)
    logging.getLogger("sqlalchemy.pool").setLevel(logging.WARNING)
    logging.getLogger("sqlalchemy.orm").setLevel(logging.WARNING)
    logging.getLogger("sqlalchemy.dialects").setLevel(logging.WARNING)


def create_formatter(
    app_name: Optional[str] = None,
    app_type: Optional[str] = None,
    domain: Optional[str] = None,
    app_env: Optional[str] = None,
) -> logging.Formatter:
    """
    Create a standardized formatter with app metadata.

    For OTEL compatibility, we keep the format simple since structured attributes
    are handled at the resource level in OTEL configuration.

    Args:
        app_name: Application name from release metadata
        app_type: Application type (e.g., 'external-api', 'internal-api', 'worker', 'job')
        domain: Domain/category for the app (e.g., 'demo', 'manman')
        app_env: Application environment (e.g., 'dev', 'staging', 'prod')

    Returns:
        Configured logging formatter
    """
    # Build context prefix with available metadata
    context_parts = []
    if domain:
        context_parts.append(domain)
    if app_name:
        context_parts.append(app_name)
    if app_type:
        context_parts.append(app_type)
    if app_env:
        context_parts.append(app_env)

    if context_parts:
        context_prefix = f"[{'/'.join(context_parts)}] "
    else:
        context_prefix = ""

    return logging.Formatter(
        f"%(asctime)s - {context_prefix}%(name)s - %(levelname)s - %(message)s"
    )


def setup_server_logging(
    app_name: Optional[str] = None,
    app_type: Optional[str] = None,
    domain: Optional[str] = None,
    app_env: Optional[str] = None,
) -> None:
    """
    Setup logging for web servers (uvicorn/gunicorn) that preserves existing handlers.

    This function configures server-specific loggers without clobbering
    the root logger configuration, allowing OTEL and other handlers to coexist.

    Args:
        app_name: Application name from release metadata
        app_type: Application type
        domain: Domain/category for the app
        app_env: Application environment
    """
    formatter = create_formatter(
        app_name=app_name,
        app_type=app_type,
        domain=domain,
        app_env=app_env,
    )

    # Create a console handler for server logs
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setFormatter(formatter)

    # Configure server-specific loggers
    server_loggers = [
        "uvicorn",
        "uvicorn.error",
        "uvicorn.access",
        "gunicorn",
        "gunicorn.access",
        "gunicorn.error",
    ]

    for logger_name in server_loggers:
        logger = logging.getLogger(logger_name)
        # Clear any existing handlers to avoid duplicates
        logger.handlers.clear()
        logger.addHandler(console_handler)
        logger.setLevel(logging.INFO)
        logger.propagate = False  # Don't propagate to root to avoid duplicate logs


def _setup_otel_logging(
    app_name: Optional[str] = None,
    app_type: Optional[str] = None,
    domain: Optional[str] = None,
    app_env: Optional[str] = None,
    otel_endpoint: Optional[str] = None,
) -> None:
    """
    Setup OTEL logging configuration with app metadata.

    Args:
        app_name: Application name from release metadata
        app_type: Application type
        domain: Domain/category for the app
        app_env: Application environment
        otel_endpoint: OTEL collector endpoint
    """
    if not OTEL_AVAILABLE:
        return

    # Build resource attributes from app metadata
    resource_attrs = {
        "service.instance.id": os.uname().nodename,
    }

    # Add release app properties
    if domain and app_name:
        # Use domain-app format for service name
        resource_attrs["service.name"] = f"{domain}-{app_name}"
    elif app_name:
        resource_attrs["service.name"] = app_name
    elif domain:
        resource_attrs["service.name"] = domain

    if app_type:
        resource_attrs["service.type"] = app_type

    if domain:
        resource_attrs["service.domain"] = domain

    # Add deployment properties
    if app_env:
        resource_attrs["deployment.environment"] = app_env

    # Create OTEL logger provider with service identification
    logger_provider = LoggerProvider(resource=Resource.create(resource_attrs))
    set_logger_provider(logger_provider)

    # Setup OTLP exporter
    endpoint = otel_endpoint or os.getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")

    otlp_exporter = OTLPLogExporter(
        endpoint=endpoint,
        insecure=True,
    )
    logger_provider.add_log_record_processor(BatchLogRecordProcessor(otlp_exporter))

    # Add OTEL handler to root logger
    handler = LoggingHandler(level=logging.NOTSET, logger_provider=logger_provider)
    logging.getLogger().addHandler(handler)


def _setup_otel_tracing(
    app_name: Optional[str] = None,
    app_type: Optional[str] = None,
    domain: Optional[str] = None,
    app_env: Optional[str] = None,
    otel_endpoint: Optional[str] = None,
) -> None:
    """
    Setup OTEL tracing configuration with app metadata.

    Args:
        app_name: Application name from release metadata
        app_type: Application type
        domain: Domain/category for the app
        app_env: Application environment
        otel_endpoint: OTEL collector endpoint
    """
    if not OTEL_AVAILABLE:
        return

    # Build resource attributes from app metadata
    resource_attrs = {
        "service.instance.id": os.uname().nodename,
    }

    # Add release app properties
    if domain and app_name:
        # Use domain-app format for service name
        resource_attrs["service.name"] = f"{domain}-{app_name}"
    elif app_name:
        resource_attrs["service.name"] = app_name
    elif domain:
        resource_attrs["service.name"] = domain

    if app_type:
        resource_attrs["service.type"] = app_type

    if domain:
        resource_attrs["service.domain"] = domain

    # Add deployment properties
    if app_env:
        resource_attrs["deployment.environment"] = app_env

    # Create OTEL tracer provider with service identification
    resource = Resource.create(resource_attrs)
    tracer_provider = TracerProvider(resource=resource)
    set_tracer_provider(tracer_provider)

    # Setup OTLP span exporter
    traces_endpoint = (
        otel_endpoint
        or os.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
        or os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
    )

    otlp_span_exporter = OTLPSpanExporter(
        endpoint=traces_endpoint,
        insecure=True,
    )
    tracer_provider.add_span_processor(BatchSpanProcessor(otlp_span_exporter))


def _setup_console_logging(
    app_name: Optional[str] = None,
    app_type: Optional[str] = None,
    domain: Optional[str] = None,
    app_env: Optional[str] = None,
) -> None:
    """
    Setup console logging configuration with app metadata.

    Args:
        app_name: Application name from release metadata
        app_type: Application type
        domain: Domain/category for the app
        app_env: Application environment
    """
    # Use the standardized formatter with app metadata
    formatter = create_formatter(
        app_name=app_name,
        app_type=app_type,
        domain=domain,
        app_env=app_env,
    )

    # Create console handler
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(formatter)

    # Add to root logger
    logging.getLogger().addHandler(handler)


def get_gunicorn_config(
    app_name: str,
    app_type: Optional[str] = None,
    domain: Optional[str] = None,
    app_env: Optional[str] = None,
    port: int = 8000,
    workers: int = 1,
    worker_class: str = "uvicorn.workers.UvicornWorker",
    preload_app: bool = True,
    enable_otel: bool = False,
) -> dict:
    """
    Get Gunicorn configuration with app metadata.

    Note: Logging configuration is handled separately in app factory functions
    to ensure proper initialization order using Python objects instead of
    dictionary-based configuration.

    Args:
        app_name: Application name from release metadata
        app_type: Application type
        domain: Domain/category for the app
        app_env: Application environment
        port: Port to bind to
        workers: Number of worker processes
        worker_class: Gunicorn worker class to use
        preload_app: Whether to preload the application before forking workers
        enable_otel: Whether OTEL logging is enabled (unused but kept for compatibility)

    Returns:
        Configuration dict for Gunicorn
    """
    # Build service display name from app metadata
    name_parts = []
    if domain:
        name_parts.append(domain)
    if app_name:
        name_parts.append(app_name)
    if app_type:
        name_parts.append(app_type)

    service_display = "/".join(name_parts) if name_parts else "app"

    # Base configuration - same for all services
    config = {
        "bind": f"0.0.0.0:{port}",
        "workers": workers,
        "worker_class": worker_class,
        "worker_connections": 1000,
        "max_requests": 1000,
        "max_requests_jitter": 100,
        "preload_app": preload_app,
        "keepalive": 2,
        "timeout": 30,
        "graceful_timeout": 30,
        # Logging format and output
        "access_log_format": f'[{service_display}] %(h)s "%(r)s" %(s)s %(b)s "%(f)s" "%(a)s" %(D)s',
        "accesslog": "-",  # Log to stdout
        "errorlog": "-",  # Log to stderr
        "loglevel": "info",
        "capture_output": True,
        "enable_stdio_inheritance": True,
    }

    return config
