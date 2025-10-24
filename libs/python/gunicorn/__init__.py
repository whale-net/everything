"""
Gunicorn configuration utilities for FastAPI applications.

This module provides reusable Gunicorn configuration for production deployments
with proper logging, worker management, and graceful shutdown.
"""

from libs.python.gunicorn.config import get_gunicorn_config

__all__ = ["get_gunicorn_config"]
