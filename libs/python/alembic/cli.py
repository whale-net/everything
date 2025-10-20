"""Consolidated CLI for database migrations.

Provides a reusable Typer application for managing database migrations.
Can be used as a standalone CLI or integrated into existing CLI applications.

Example usage in your CLI:
    ```python
    import typer
    from libs.python.alembic.cli import create_migration_app
    from myapp.models import Base

    app = typer.Typer()

    # Create migration CLI with your configuration
    migration_cli = create_migration_app(
        migrations_package="myapp.migrations",
        target_metadata=Base.metadata,
    )

    # Add to your main CLI
    app.add_typer(migration_cli, name="migration")

    if __name__ == "__main__":
        app()
    ```

The migration app uses @pg_params decorator which injects POSTGRES_URL automatically.
"""

import logging
import os
from typing import Annotated, Callable, Optional

import typer
from sqlalchemy import MetaData, create_engine

try:
    from importlib.resources import files
except ImportError:
    # Python < 3.9 fallback
    from importlib_resources import files

from libs.python.alembic.config import create_alembic_config
from libs.python.alembic.migration import (
    create_migration,
    run_downgrade,
    run_migration,
    should_run_migration,
)
from libs.python.cli.params import pg_params

logger = logging.getLogger(__name__)


def create_migration_app(
    migrations_package: str,
    target_metadata: Optional[MetaData] = None,
    include_object: Optional[Callable] = None,
    version_table_schema: str = "public",
) -> typer.Typer:
    """Create a Typer CLI application for database migrations.

    Args:
        migrations_package: Python package path to migrations (e.g., "myapp.migrations")
        target_metadata: SQLAlchemy MetaData object. If None, must be provided by migrations/env.py
        include_object: Optional callback for filtering objects during autogenerate
        version_table_schema: Schema for alembic_version table

    Returns:
        Configured Typer application with @pg_params decorator applied

    Example:
        >>> from myapp.models import Base
        >>> app = create_migration_app(
        ...     migrations_package="myapp.migrations",
        ...     target_metadata=Base.metadata,
        ... )
        >>> # Database URL comes from POSTGRES_URL env var via @pg_params
    """
    migration_app = typer.Typer(
        help=f"Database migration commands for {migrations_package}",
        context_settings={"obj": {}},
    )

    # Add callback with pg_params decorator to inject POSTGRES_URL
    @migration_app.callback()
    @pg_params
    def callback(ctx: typer.Context):
        """Database migration commands."""
        # pg_params decorator injects postgres config into ctx.obj
        pass

    def _get_migrations_dir() -> str:
        """Get the migrations directory path."""
        try:
            migrations_resource = files(migrations_package)
            
            # Use public API to get filesystem path
            # Check if the resource supports __fspath__ before calling os.fspath
            if hasattr(migrations_resource, "__fspath__"):
                migrations_dir = os.fspath(migrations_resource)
            else:
                # Fallback for resource types that don't support fspath
                migrations_dir = str(migrations_resource)

            # Verify the directory exists and has env.py
            env_py_path = os.path.join(migrations_dir, "env.py")
            if not os.path.exists(env_py_path):
                raise FileNotFoundError(
                    f"env.py not found in migrations directory: {migrations_dir}"
                )

            return migrations_dir

        except (TypeError, AttributeError, ModuleNotFoundError) as e:
            logger.error(f"Failed to locate migrations directory: {e}")
            raise RuntimeError(
                f"Could not locate migrations directory for package: {migrations_package}. "
                f"Ensure the package is properly installed and accessible."
            ) from e

    @migration_app.command("run")
    def run_cmd(
        ctx: typer.Context,
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Run pending database migrations to head."""
        logger.info("Running database migrations")

        # Get database URL from pg_params context
        db_url = ctx.obj.get("postgres", {}).get("database_url")
        if not db_url:
            raise RuntimeError("Database URL not found in context. Ensure POSTGRES_URL is set.")
        
        migrations_dir = _get_migrations_dir()

        engine = create_engine(url=db_url, echo=echo, pool_pre_ping=True)
        config = create_alembic_config(
            migrations_dir=migrations_dir,
            database_url=db_url,
            version_table_schema=version_table_schema,
        )

        run_migration(engine, config)
        logger.info("Migrations completed successfully")

    @migration_app.command("check")
    def check_cmd(
        ctx: typer.Context,
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Check if there are pending migrations."""
        logger.info("Checking for pending migrations")

        # Get database URL from pg_params context
        db_url = ctx.obj.get("postgres", {}).get("database_url")
        if not db_url:
            raise RuntimeError("Database URL not found in context. Ensure POSTGRES_URL is set.")
        
        migrations_dir = _get_migrations_dir()

        engine = create_engine(url=db_url, echo=echo, pool_pre_ping=True)
        config = create_alembic_config(
            migrations_dir=migrations_dir,
            database_url=db_url,
            version_table_schema=version_table_schema,
        )

        if should_run_migration(engine, config):
            logger.warning("Pending migrations detected")
            raise typer.Exit(code=1)
        else:
            logger.info("No pending migrations")
            typer.echo("✓ Database is up to date")

    @migration_app.command("create")
    def create_cmd(
        ctx: typer.Context,
        message: Annotated[Optional[str], typer.Argument(help="Migration message")],
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Create a new migration based on model changes."""
        logger.info(f"Creating migration: {message or '(no message)'}")

        # Get database URL from pg_params context
        db_url = ctx.obj.get("postgres", {}).get("database_url")
        if not db_url:
            raise RuntimeError("Database URL not found in context. Ensure POSTGRES_URL is set.")
        
        migrations_dir = _get_migrations_dir()

        engine = create_engine(url=db_url, echo=echo, pool_pre_ping=True)
        config = create_alembic_config(
            migrations_dir=migrations_dir,
            database_url=db_url,
            version_table_schema=version_table_schema,
        )

        try:
            create_migration(engine, config, message)
            logger.info("Migration created successfully")
        except RuntimeError as e:
            logger.error(f"Failed to create migration: {e}")
            raise typer.Exit(code=1)

    @migration_app.command("downgrade")
    def downgrade_cmd(
        ctx: typer.Context,
        revision: Annotated[
            str, typer.Argument(help="Target revision (e.g., -1, base, or revision hash)")
        ],
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Downgrade database to a specific revision."""
        logger.info(f"Downgrading to revision: {revision}")

        # Get database URL from pg_params context
        db_url = ctx.obj.get("postgres", {}).get("database_url")
        if not db_url:
            raise RuntimeError("Database URL not found in context. Ensure POSTGRES_URL is set.")
        
        migrations_dir = _get_migrations_dir()

        engine = create_engine(url=db_url, echo=echo, pool_pre_ping=True)
        config = create_alembic_config(
            migrations_dir=migrations_dir,
            database_url=db_url,
            version_table_schema=version_table_schema,
        )

        run_downgrade(engine, config, revision)
        logger.info("Downgrade completed successfully")

    return migration_app


# Default CLI for standalone usage
if __name__ == "__main__":
    import sys

    # This is for standalone usage when migrations_package is provided via CLI args
    app = typer.Typer(help="Database migration CLI")

    @app.callback()
    def main_callback():
        """Consolidated database migration CLI."""
        pass

    # Note: For actual usage, create an app with create_migration_app()
    typer.echo(
        "This module should be imported and configured with your migrations package.\n"
        "Example:\n"
        "  from libs.python.alembic.cli import create_migration_app\n"
        "  app = create_migration_app(migrations_package='myapp.migrations')\n"
        "  app()\n"
    )
    sys.exit(1)
