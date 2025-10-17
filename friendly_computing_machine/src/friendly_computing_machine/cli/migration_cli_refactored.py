"""
Refactored migration CLI using dependency injection pattern.

This is an example of how to refactor an existing CLI module to use the
new dependency injection system. Compare this with the original migration_cli.py
to see the benefits.

Original pattern (migration_cli.py):
- Requires callback function with all parameters
- Manual setup_db() call in callback
- Access DB via ctx.obj[DB_FILENAME]

New pattern (this file):
- No callback needed!
- Dependencies injected directly into commands
- Type-safe access to DBContext

Benefits:
- Less boilerplate code
- Better type safety
- Cleaner command functions
- Easier to test
"""

import logging
from typing import Annotated, Optional

import typer

from friendly_computing_machine.src.friendly_computing_machine.cli.context.db import (
    DBContext,
)
from libs.python.cli.deps import (
    Depends,
    inject_dependencies,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.injectable import (
    get_db_context,
    get_logging_config,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    create_migration,
    run_downgrade,
    run_migration,
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

logger = logging.getLogger(__name__)

migration_app_refactored = typer.Typer(
    context_settings={"obj": {}},
    help="Database migration commands (refactored with dependency injection)",
)


# No callback needed! Dependencies are injected directly into commands.
# Compare this with the original migration_cli.py which has a callback
# that manually calls setup_logging() and setup_db().


@migration_app_refactored.command("run")
@inject_dependencies
def cli_migration_run(
    ctx: typer.Context,
    # Dependencies are automatically injected
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    log_config: Annotated[dict, Depends(get_logging_config)] = None,
):
    """
    Run database migrations.
    
    This command demonstrates dependency injection in action.
    Notice how DBContext is automatically injected - no manual setup needed!
    """
    logger.info("running migration")
    run_migration(db_ctx.engine, db_ctx.alembic_config)
    logger.info("migration complete")


@migration_app_refactored.command("create")
@inject_dependencies
def cli_migration_create(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    message: Optional[str] = None,
):
    """
    Create a new migration.
    
    Args:
        message: Optional migration message
    """
    logger.info("creating migration")
    create_migration(db_ctx.engine, db_ctx.alembic_config, message)
    logger.info("migration created")


@migration_app_refactored.command("downgrade")
@inject_dependencies
def cli_migration_downgrade(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    revision: str = typer.Argument(..., help="Target revision to downgrade to"),
):
    """
    Downgrade database to a specific revision.
    
    Args:
        revision: Target revision (e.g., 'head-1', revision hash)
    """
    logger.info("downgrading migration")
    run_downgrade(db_ctx.engine, db_ctx.alembic_config, revision)
    logger.info("migration downgraded")


# Example of a new command that benefits from dependency injection
@migration_app_refactored.command("status")
@inject_dependencies
def cli_migration_status(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    """
    Show current migration status.
    
    This is a new command that demonstrates how easy it is to add
    new commands when using dependency injection - just inject what you need!
    """
    from alembic import command as alembic_command
    
    logger.info("checking migration status")
    
    # Show current revision
    alembic_command.current(db_ctx.alembic_config)
    
    # Show migration history
    print("\nMigration History:")
    alembic_command.history(db_ctx.alembic_config)
    
    logger.info("migration status complete")


if __name__ == "__main__":
    migration_app_refactored()
