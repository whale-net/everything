"""
Dependency injection system for Typer CLI applications.

DEPRECATED: This module has been moved to //libs/python/cli/deps.py

Please update your imports to:
    from libs.python.cli.deps import Depends, inject_dependencies, injectable

This file will be removed in a future release.
"""

# Re-export from new location for backward compatibility
from libs.python.cli.deps import (  # noqa: F401
    Depends,
    create_dependency,
    inject_dependencies,
    injectable,
)

__all__ = ["Depends", "inject_dependencies", "injectable", "create_dependency"]
