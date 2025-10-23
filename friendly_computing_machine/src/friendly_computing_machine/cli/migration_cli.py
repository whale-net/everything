"""Migration CLI for friendly_computing_machine using consolidated library."""

import logging

import typer

from libs.python.alembic.cli import create_migration_app
from libs.python.cli.params import pg_params, logging_params

# Import all models to ensure they are registered with SQLAlchemy
from friendly_computing_machine.src.friendly_computing_machine.models import (  # noqa: F401
    base,
    genai,
    manman,
    music_poll,
    slack,
    task,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import Base

logger = logging.getLogger(__name__)

# Create migration CLI using consolidated library
migration_app = create_migration_app(
    migrations_package="friendly_computing_machine.src.migrations",
    target_metadata=Base.metadata,
    version_table_schema="public",
)


# Add callback for logging setup
@migration_app.callback()
@pg_params
@logging_params  # Auto-configures logging from environment variables
def callback(ctx: typer.Context):
    """Migration commands for friendly_computing_machine database."""
    pass
