"""
Gunicorn configuration for FastAPI applications.

Provides production-ready Gunicorn settings with proper logging,
worker management, and graceful shutdown.
"""

import logging
import os


def _configure_worker_logging(server, worker):
    """
    Configure logging in each worker process using the consolidated logging library.
    
    This hook is called after each worker process is forked. It ensures that each
    worker uses the same OTLP-enabled logging configuration as the main application.
    
    Args:
        server: Gunicorn server instance
        worker: Worker instance being initialized
    """
    from libs.python.logging import configure_logging, is_configured
    
    # Only configure if not already done (e.g., if preload_app=False)
    if not is_configured():
        # Auto-detect configuration from environment variables
        # This picks up APP_NAME, APP_VERSION, APP_ENV, etc.
        configure_logging(
            log_level=os.getenv("LOG_LEVEL", "INFO"),
            enable_otlp=os.getenv("LOG_OTLP", "").lower() in ("true", "1", "yes"),
            json_format=os.getenv("LOG_JSON_FORMAT", "").lower() in ("true", "1", "yes"),
            force_reconfigure=False,
        )
        logging.debug(f"Configured logging in worker {worker.pid}")


def get_gunicorn_config(
    microservice_name: str,
    port: int = 8000,
    workers: int = 1,
    worker_class: str = "libs.python.gunicorn.uvicorn_worker.UvicornWorker",
    preload_app: bool = True,
    enable_otel: bool = False,
) -> dict:
    """
    Get Gunicorn configuration for FastAPI services.

    This configuration integrates with the consolidated logging library
    (libs.python.logging) to ensure that all logs, including those from
    gunicorn and uvicorn, are sent via OTLP when enabled.

    Args:
        microservice_name: Name of the microservice component for identification
        port: Port to bind to (default: 8000)
        workers: Number of worker processes (default: 1)
        worker_class: Gunicorn worker class to use (default: libs.python.gunicorn.uvicorn_worker.UvicornWorker)
        preload_app: Whether to preload the application before forking workers (default: True)
        enable_otel: Whether OTEL logging is enabled (deprecated, use LOG_OTLP env var)

    Returns:
        Configuration dict for Gunicorn

    Example:
        >>> from libs.python.gunicorn import get_gunicorn_config
        >>> options = get_gunicorn_config(
        ...     microservice_name="my-api",
        ...     port=8000,
        ...     workers=4,
        ... )
        >>> GunicornApplication(create_app, options).run()
    """
    # Build service display name for logs
    service_display = microservice_name

    # Base configuration - production-ready defaults
    config = {
        "bind": f"0.0.0.0:{port}",
        "workers": workers,
        "worker_class": worker_class,
        "worker_connections": 1000,
        "max_requests": 1000,  # Restart workers after N requests to prevent memory leaks
        "max_requests_jitter": 100,  # Add randomness to max_requests to avoid thundering herd
        "preload_app": preload_app,
        "keepalive": 2,
        "timeout": 30,  # Worker timeout
        "graceful_timeout": 30,  # Graceful shutdown timeout
        # Logging format and output
        "access_log_format": f'[{service_display}] %(h)s "%(r)s" %(s)s %(b)s "%(f)s" "%(a)s" %(D)s',
        "accesslog": "-",  # Log to stdout
        "errorlog": "-",  # Log to stderr
        "loglevel": "info",
        "capture_output": True,
        "enable_stdio_inheritance": True,
        # Integrate with consolidated logging library via post_fork hook
        "post_fork": _configure_worker_logging,
    }

    return config
