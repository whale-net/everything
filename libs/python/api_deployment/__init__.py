"""
Default deployment configuration for Python API applications.

This module provides production-ready configuration for deploying FastAPI
and other ASGI applications using gunicorn with uvicorn workers.
"""

from libs.python.api_deployment.config import (
    get_default_gunicorn_config,
    run_with_gunicorn,
)
from libs.python.api_deployment.cli import create_deployment_cli

__all__ = [
    "get_default_gunicorn_config",
    "run_with_gunicorn",
    "create_deployment_cli",
]
