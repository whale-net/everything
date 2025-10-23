"""Migration CLI for friendly_computing_machine using consolidated library."""

import logging

import typer

from libs.python.alembic.cli import create_migration_app
from libs.python.cli.params import pg_params, logging_params
from libs.python.logging import configure_logging

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
@logging_params
def callback(ctx: typer.Context):
    """Migration commands for friendly_computing_machine database."""
    # Configure OTLP-first logging (with CLI flag override)
    log_config = ctx.obj.get("logging", {})
    configure_logging(
        service_name="friendly-computing-machine-migrations",
        service_version="1.0.0",
        deployment_environment="dev",
        log_level="DEBUG",
        enable_otlp=log_config.get("enable_otlp", True),  # Default True, CLI can override
        json_format=False,
    )
