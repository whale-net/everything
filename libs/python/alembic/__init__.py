"""Consolidated Alembic configuration library.

This library provides reusable components for database migrations using Alembic.
It consolidates common patterns from manman and friendly_computing_machine projects.
"""

from libs.python.alembic.cli import create_migration_app
from libs.python.alembic.config import create_alembic_config
from libs.python.alembic.migration import (
    create_migration,
    run_downgrade,
    run_migration,
    should_run_migration,
)

__all__ = [
    "create_alembic_config",
    "create_migration",
    "create_migration_app",
    "run_downgrade",
    "run_migration",
    "should_run_migration",
]
