"""
Command-line interface for running API applications with gunicorn.

Provides a simple CLI that can be used to run any ASGI application
with production-ready gunicorn configuration.
"""

import argparse
import sys
from typing import Optional


def create_deployment_cli(
    app_module: str,
    app_name: str = "api",
    default_port: int = 8000,
    description: Optional[str] = None,
) -> argparse.ArgumentParser:
    """
    Create a command-line interface for deploying an API application.
    
    This function creates an argument parser configured for running an
    API application with gunicorn. It can be used in application entry points
    to provide consistent CLI options.
    
    Args:
        app_module: Module path to the ASGI application (e.g., "main:app")
        app_name: Application name (default: "api")
        default_port: Default port number (default: 8000)
        description: CLI description (optional)
    
    Returns:
        Configured ArgumentParser instance
    
    Example:
        >>> # In your main.py
        >>> from fastapi import FastAPI
        >>> from libs.python.api_deployment import create_deployment_cli
        >>> 
        >>> app = FastAPI()
        >>> 
        >>> if __name__ == "__main__":
        >>>     parser = create_deployment_cli(
        >>>         app_module="main:app",
        >>>         app_name="my-api",
        >>>         description="My API Application"
        >>>     )
        >>>     args = parser.parse_args()
        >>>     
        >>>     if args.production:
        >>>         # Run with gunicorn
        >>>         from libs.python.api_deployment import run_with_gunicorn
        >>>         run_with_gunicorn(
        >>>             "main:app",
        >>>             app_name="my-api",
        >>>             host=args.host,
        >>>             port=args.port,
        >>>             workers=args.workers,
        >>>         )
        >>>     else:
        >>>         # Run with uvicorn for development
        >>>         import uvicorn
        >>>         uvicorn.run(app, host=args.host, port=args.port)
    """
    if description is None:
        description = f"Run {app_name} API application"
    
    parser = argparse.ArgumentParser(description=description)
    
    parser.add_argument(
        "--host",
        type=str,
        default="0.0.0.0",
        help="Host to bind to (default: 0.0.0.0)",
    )
    
    parser.add_argument(
        "--port",
        type=int,
        default=default_port,
        help=f"Port to bind to (default: {default_port})",
    )
    
    parser.add_argument(
        "--production",
        action="store_true",
        help="Run in production mode with gunicorn (default: development mode with uvicorn)",
    )
    
    parser.add_argument(
        "--workers",
        type=int,
        default=None,
        help="Number of gunicorn workers (default: auto-calculate from CPU cores)",
    )
    
    parser.add_argument(
        "--timeout",
        type=int,
        default=30,
        help="Worker timeout in seconds (default: 30, only for production mode)",
    )
    
    parser.add_argument(
        "--log-level",
        type=str,
        default="info",
        choices=["debug", "info", "warning", "error", "critical"],
        help="Logging level (default: info)",
    )
    
    return parser


def run_from_cli(
    app_module: str,
    app_name: str = "api",
    default_port: int = 8000,
    description: Optional[str] = None,
) -> None:
    """
    Run an API application from command line with automatic mode selection.
    
    This is a convenience function that combines CLI parsing with application
    execution. It automatically chooses between development (uvicorn) and
    production (gunicorn) modes based on the --production flag.
    
    Args:
        app_module: Module path to the ASGI application (e.g., "main:app")
        app_name: Application name (default: "api")
        default_port: Default port number (default: 8000)
        description: CLI description (optional)
    
    Example:
        >>> # In your main.py
        >>> from fastapi import FastAPI
        >>> from libs.python.api_deployment.cli import run_from_cli
        >>> 
        >>> app = FastAPI()
        >>> 
        >>> if __name__ == "__main__":
        >>>     run_from_cli("main:app", app_name="my-api")
    """
    parser = create_deployment_cli(app_module, app_name, default_port, description)
    args = parser.parse_args()
    
    if args.production:
        # Run in production mode with gunicorn
        from libs.python.api_deployment.config import run_with_gunicorn
        
        print(f"Starting {app_name} in production mode with gunicorn...")
        print(f"  Host: {args.host}")
        print(f"  Port: {args.port}")
        print(f"  Workers: {args.workers or 'auto'}")
        print(f"  Log level: {args.log_level}")
        
        run_with_gunicorn(
            app_module,
            app_name=app_name,
            host=args.host,
            port=args.port,
            workers=args.workers,
            timeout=args.timeout,
            loglevel=args.log_level,
        )
    else:
        # Run in development mode with uvicorn
        try:
            import uvicorn
        except ImportError:
            print(
                "ERROR: uvicorn is not installed. "
                "Please install it: pip install uvicorn",
                file=sys.stderr,
            )
            sys.exit(1)
        
        print(f"Starting {app_name} in development mode with uvicorn...")
        print(f"  Host: {args.host}")
        print(f"  Port: {args.port}")
        print(f"  Log level: {args.log_level}")
        
        # Import the application
        module_path, variable_name = app_module.split(":", 1)
        __import__(module_path)
        module = sys.modules[module_path]
        app = getattr(module, variable_name)
        
        uvicorn.run(
            app,
            host=args.host,
            port=args.port,
            log_level=args.log_level,
        )
