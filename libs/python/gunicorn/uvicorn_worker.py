"""
Custom Uvicorn worker that integrates with the consolidated logging library.

This worker class disables Uvicorn's default logging configuration and uses
the logging setup from libs.python.logging instead, ensuring all logs are
sent via OTLP when enabled.
"""

try:
    from uvicorn.workers import UvicornWorker as BaseUvicornWorker
    
    class UvicornWorker(BaseUvicornWorker):
        """
        Custom Uvicorn worker that disables Uvicorn's logging configuration.
        
        This ensures that the consolidated logging library (libs.python.logging)
        controls all logging, including access logs and error logs, and that
        logs are properly sent via OTLP.
        
        Usage:
            >>> options = get_gunicorn_config(
            ...     microservice_name="my-api",
            ...     worker_class="libs.python.gunicorn.uvicorn_worker.UvicornWorker",
            ... )
        """
        
        CONFIG_KWARGS = {
            # Disable uvicorn's logging configuration
            # This prevents uvicorn from setting up its own handlers
            "log_config": None,
        }
    
    UVICORN_AVAILABLE = True
except ImportError:
    UVICORN_AVAILABLE = False
    UvicornWorker = None
