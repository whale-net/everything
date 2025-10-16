"""
Default gunicorn configuration for Python API applications.

Provides production-ready settings for running ASGI applications
(FastAPI, etc.) using gunicorn with uvicorn workers.
"""

import logging
import multiprocessing
import os
import sys
from typing import Any, Callable, Dict, Optional


def get_default_gunicorn_config(
    app_name: str = "api",
    host: str = "0.0.0.0",
    port: int = 8000,
    workers: Optional[int] = None,
    worker_class: str = "uvicorn.workers.UvicornWorker",
    log_level: str = "info",
    timeout: int = 30,
    keepalive: int = 2,
    max_requests: int = 1000,
    max_requests_jitter: int = 100,
    **kwargs: Any,
) -> Dict[str, Any]:
    """
    Get default gunicorn configuration for API deployment.
    
    This configuration provides sensible defaults for production deployments:
    - Auto-scales workers based on CPU cores (2 * cores + 1)
    - Uses uvicorn workers for ASGI compatibility
    - Configures timeouts and request limits for stability
    - Sets up proper logging to stdout/stderr
    
    Args:
        app_name: Application name for logging (default: "api")
        host: Host to bind to (default: "0.0.0.0")
        port: Port to bind to (default: 8000)
        workers: Number of worker processes (default: auto-calculate from CPU cores)
        worker_class: Gunicorn worker class (default: "uvicorn.workers.UvicornWorker")
        log_level: Logging level (default: "info")
        timeout: Worker timeout in seconds (default: 30)
        keepalive: Keepalive time in seconds (default: 2)
        max_requests: Max requests per worker before restart (default: 1000)
        max_requests_jitter: Jitter for max_requests (default: 100)
        **kwargs: Additional gunicorn configuration options
    
    Returns:
        Configuration dictionary for gunicorn
    
    Example:
        >>> config = get_default_gunicorn_config(app_name="my-api", port=8080)
        >>> # Use with GunicornApplication class or gunicorn directly
    """
    # Auto-calculate optimal number of workers if not specified
    # Formula: (2 * CPU cores) + 1 - common best practice
    if workers is None:
        workers = (multiprocessing.cpu_count() * 2) + 1
    
    # Base configuration with sensible production defaults
    config = {
        "bind": f"{host}:{port}",
        "workers": workers,
        "worker_class": worker_class,
        "worker_connections": 1000,
        "timeout": timeout,
        "keepalive": keepalive,
        "max_requests": max_requests,
        "max_requests_jitter": max_requests_jitter,
        "preload_app": False,  # Don't preload to allow per-worker initialization
        "accesslog": "-",  # Log to stdout
        "errorlog": "-",  # Log to stderr
        "loglevel": log_level,
        "access_log_format": (
            f'[{app_name}] %(h)s "%(r)s" %(s)s %(b)s "%(f)s" "%(a)s" %(D)s'
        ),
        "capture_output": True,
        "enable_stdio_inheritance": True,
    }
    
    # Merge any additional configuration options
    config.update(kwargs)
    
    return config


def run_with_gunicorn(
    app: str,
    app_name: str = "api",
    host: str = "0.0.0.0",
    port: int = 8000,
    workers: Optional[int] = None,
    **kwargs: Any,
) -> None:
    """
    Run an ASGI application with gunicorn using default configuration.
    
    This is a convenience function that imports and runs gunicorn with the
    default configuration. It's suitable for use in application entry points.
    
    Args:
        app: Application module path (e.g., "my_module.main:app")
        app_name: Application name for logging (default: "api")
        host: Host to bind to (default: "0.0.0.0")
        port: Port to bind to (default: 8000)
        workers: Number of worker processes (default: auto-calculate)
        **kwargs: Additional gunicorn configuration options
    
    Example:
        >>> # In your main.py
        >>> from fastapi import FastAPI
        >>> app = FastAPI()
        >>> 
        >>> if __name__ == "__main__":
        >>>     from libs.python.api_deployment import run_with_gunicorn
        >>>     run_with_gunicorn("main:app", app_name="my-api", port=8080)
    """
    try:
        from gunicorn.app.base import BaseApplication
    except ImportError:
        print(
            "ERROR: gunicorn is not installed. "
            "Please install it: pip install gunicorn",
            file=sys.stderr,
        )
        sys.exit(1)
    
    # Get default configuration
    config = get_default_gunicorn_config(
        app_name=app_name,
        host=host,
        port=port,
        workers=workers,
        **kwargs,
    )
    
    class GunicornApplication(BaseApplication):
        """Custom Gunicorn application for running ASGI apps."""
        
        def __init__(self, app_uri: str, options: Dict[str, Any]):
            self.app_uri = app_uri
            self.options = options or {}
            super().__init__()
        
        def load_config(self) -> None:
            """Load configuration from options dict."""
            for key, value in self.options.items():
                if key in self.cfg.settings and value is not None:
                    self.cfg.set(key.lower(), value)
        
        def load(self) -> Callable:
            """Load the ASGI application."""
            # Import the application using the standard module:variable syntax
            module_path, variable_name = self.app_uri.split(":", 1)
            __import__(module_path)
            module = sys.modules[module_path]
            return getattr(module, variable_name)
    
    # Create and run the gunicorn application
    gunicorn_app = GunicornApplication(app, config)
    gunicorn_app.run()


def setup_logging(
    level: int = logging.INFO,
    app_name: Optional[str] = None,
) -> None:
    """
    Setup basic logging configuration for API applications.
    
    This provides a simple logging setup suitable for containerized deployments
    where logs are captured from stdout/stderr.
    
    Args:
        level: Logging level (default: INFO)
        app_name: Application name to include in log format (optional)
    """
    # Check if logging is already configured
    root_logger = logging.getLogger()
    if root_logger.handlers:
        return
    
    # Create formatter
    if app_name:
        log_format = f"%(asctime)s - [{app_name}] %(name)s - %(levelname)s - %(message)s"
    else:
        log_format = "%(asctime)s - %(name)s - %(levelname)s - %(message)s"
    
    formatter = logging.Formatter(log_format)
    
    # Setup console handler
    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setFormatter(formatter)
    
    # Configure root logger
    root_logger.addHandler(console_handler)
    root_logger.setLevel(level)
    
    # Reduce noise from common libraries
    logging.getLogger("uvicorn.access").setLevel(logging.WARNING)
    logging.getLogger("uvicorn.error").setLevel(logging.INFO)
