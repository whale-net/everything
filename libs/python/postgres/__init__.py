"""
PostgreSQL/SQLModel database utilities for the Everything monorepo.

This module provides reusable database session management patterns
that prevent connection leaks and work across multiple projects.
"""

from libs.python.postgres.repository import DatabaseRepository

__all__ = ["DatabaseRepository"]
