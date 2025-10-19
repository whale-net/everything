"""Migration CLI for friendly_computing_machine using consolidated library."""

import logging
from typing import Optional

import typer
from sqlalchemy import create_engine

try:
    from importlib.resources import files
except ImportError:
    from importlib_resources import files

from libs.python.alembic import (
    create_alembic_config,
    create_migration as create_migration_util,
    run_downgrade as run_downgrade_util,
    run_migration as run_migration_util,
)
from libs.python.cli.params import logging_params, pg_params
from libs.python.cli.providers.logging import create_logging_context

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

# Create migration app manually to use pg_params decorator pattern
migration_app = typer.Typer(
    help="Database migration commands for friendly_computing_machine",
    context_settings={"obj": {}},
)


@migration_app.callback()
@pg_params
@logging_params
def callback(ctx: typer.Context):
    """Migration commands for friendly_computing_machine database."""
    # Setup logging from decorator-injected params
    log_config = ctx.obj.get('logging', {})
    create_logging_context(
        service_name="friendly-computing-machine-migrations",
        log_level="DEBUG",
        enable_otlp=log_config.get('enable_otlp', False),
    )


def _get_alembic_config(database_url: str):
    """Get Alembic configuration using consolidated library."""
    migrations_dir = str(files("friendly_computing_machine.src.migrations"))
    
    return create_alembic_config(
        migrations_dir=migrations_dir,
        database_url=database_url,
        version_table_schema="public",
    )


@migration_app.command("run")
def run_cmd(ctx: typer.Context):
    """Run pending database migrations to head."""
    logger.info("Running database migrations")
    
    # Get database URL from postgres context injected by pg_params
    db_url = ctx.obj.get("postgres", {}).get("database_url")
    if not db_url:
        raise RuntimeError("Database URL not found in context. Ensure POSTGRES_URL is set.")
    
    engine = create_engine(url=db_url, echo=False, pool_pre_ping=True)
    config = _get_alembic_config(db_url)
    
    run_migration_util(engine, config)
    logger.info("Migrations completed successfully")


@migration_app.command("create")
def create_cmd(
    ctx: typer.Context,
    message: Optional[str] = typer.Argument(None, help="Migration message"),
):
    """Create a new migration based on model changes."""
    logger.info(f"Creating migration: {message or '(no message)'}")
    
    # Get database URL from postgres context injected by pg_params
    db_url = ctx.obj.get("postgres", {}).get("database_url")
    if not db_url:
        raise RuntimeError("Database URL not found in context. Ensure POSTGRES_URL is set.")
    
    engine = create_engine(url=db_url, echo=False, pool_pre_ping=True)
    config = _get_alembic_config(db_url)
    
    try:
        create_migration_util(engine, config, message)
        logger.info("Migration created successfully")
    except RuntimeError as e:
        logger.error(f"Failed to create migration: {e}")
        raise typer.Exit(code=1)


@migration_app.command("downgrade")
def downgrade_cmd(
    ctx: typer.Context,
    revision: str = typer.Argument(..., help="Target revision (e.g., -1, base, or revision hash)"),
):
    """Downgrade database to a specific revision."""
    logger.info(f"Downgrading to revision: {revision}")
    
    # Get database URL from postgres context injected by pg_params
    db_url = ctx.obj.get("postgres", {}).get("database_url")
    if not db_url:
        raise RuntimeError("Database URL not found in context. Ensure POSTGRES_URL is set.")
    
    engine = create_engine(url=db_url, echo=False, pool_pre_ping=True)
    config = _get_alembic_config(db_url)
    
    run_downgrade_util(engine, config, revision)
    logger.info("Downgrade completed successfully")
