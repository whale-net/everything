"""
Gunicorn configuration for FastAPI applications.

Provides production-ready Gunicorn settings with proper logging,
worker management, and graceful shutdown.
"""


def get_gunicorn_config(
    microservice_name: str,
    port: int = 8000,
    workers: int = 1,
    worker_class: str = "uvicorn.workers.UvicornWorker",
    preload_app: bool = True,
    enable_otel: bool = False,
) -> dict:
    """
    Get Gunicorn configuration for FastAPI services.

    Note: Logging configuration should be handled separately via CLI providers
    to ensure proper initialization order. This config only sets up Gunicorn's
    own logging format and output destinations.

    Args:
        microservice_name: Name of the microservice component for identification
        port: Port to bind to (default: 8000)
        workers: Number of worker processes (default: 1)
        worker_class: Gunicorn worker class to use (default: uvicorn.workers.UvicornWorker)
        preload_app: Whether to preload the application before forking workers (default: True)
        enable_otel: Whether OTEL logging is enabled (unused but kept for compatibility)

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
    }

    return config
