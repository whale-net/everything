"""Migration CLI for friendly_computing_machine using consolidated library."""

import logging

import typer

from libs.python.alembic.cli import create_migration_app
from friendly_computing_machine.src.friendly_computing_machine.cli.context.log import (
    setup_logging,
)

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
    database_url_envvar="DATABASE_URL",
    version_table_schema="public",
)


# Add callback for logging setup
@migration_app.callback(invoke_without_command=True)
def callback(
    ctx: typer.Context,
    log_otlp: bool = typer.Option(False, help="Enable OpenTelemetry logging"),
):
    """Migration commands for friendly_computing_machine database."""
    # Setup logging for all migration commands
    setup_logging(ctx, log_otlp=log_otlp)
