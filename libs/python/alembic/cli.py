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
        database_url_envvar="DATABASE_URL",
    )

    # Add to your main CLI
    app.add_typer(migration_cli, name="migration")

    if __name__ == "__main__":
        app()
    ```

Or use directly:
    ```bash
    python -m libs.python.alembic.cli --migrations-package myapp.migrations \\
        --database-url postgresql://... run
    ```
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

logger = logging.getLogger(__name__)


def create_migration_app(
    migrations_package: str,
    target_metadata: Optional[MetaData] = None,
    database_url_envvar: str = "DATABASE_URL",
    include_object: Optional[Callable] = None,
    version_table_schema: str = "public",
) -> typer.Typer:
    """Create a Typer CLI application for database migrations.

    Args:
        migrations_package: Python package path to migrations (e.g., "myapp.migrations")
        target_metadata: SQLAlchemy MetaData object. If None, must be provided by migrations/env.py
        database_url_envvar: Environment variable name for database URL
        include_object: Optional callback for filtering objects during autogenerate
        version_table_schema: Schema for alembic_version table

    Returns:
        Configured Typer application

    Example:
        >>> from myapp.models import Base
        >>> app = create_migration_app(
        ...     migrations_package="myapp.migrations",
        ...     target_metadata=Base.metadata,
        ... )
        >>> # Use as: app() or integrate into larger CLI
    """
    migration_app = typer.Typer(
        help=f"Database migration commands for {migrations_package}",
        context_settings={"obj": {}},
    )

    def _get_migrations_dir() -> str:
        """Get the migrations directory path."""
        try:
            migrations_resource = files(migrations_package)
            
            # Use public API to get filesystem path
            try:
                migrations_dir = os.fspath(migrations_resource)
            except TypeError:
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

    def _get_database_url(explicit_url: Optional[str] = None) -> str:
        """Get database URL from explicit parameter or environment."""
        if explicit_url:
            return explicit_url

        db_url = os.environ.get(database_url_envvar)
        if not db_url:
            raise RuntimeError(
                f"Database URL not found. Set {database_url_envvar} environment variable "
                f"or provide --database-url parameter."
            )
        return db_url

    @migration_app.command("run")
    def run_cmd(
        database_url: Annotated[
            Optional[str],
            typer.Option(
                "--database-url",
                envvar=database_url_envvar,
                help=f"Database URL (or set {database_url_envvar} env var)",
            ),
        ] = None,
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Run pending database migrations to head."""
        logger.info("Running database migrations")

        db_url = _get_database_url(database_url)
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
        database_url: Annotated[
            Optional[str],
            typer.Option(
                "--database-url",
                envvar=database_url_envvar,
                help=f"Database URL (or set {database_url_envvar} env var)",
            ),
        ] = None,
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Check if there are pending migrations."""
        logger.info("Checking for pending migrations")

        db_url = _get_database_url(database_url)
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
        message: Annotated[Optional[str], typer.Argument(help="Migration message")],
        database_url: Annotated[
            Optional[str],
            typer.Option(
                "--database-url",
                envvar=database_url_envvar,
                help=f"Database URL (or set {database_url_envvar} env var)",
            ),
        ] = None,
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Create a new migration based on model changes."""
        logger.info(f"Creating migration: {message or '(no message)'}")

        db_url = _get_database_url(database_url)
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
        revision: Annotated[
            str, typer.Argument(help="Target revision (e.g., -1, base, or revision hash)")
        ],
        database_url: Annotated[
            Optional[str],
            typer.Option(
                "--database-url",
                envvar=database_url_envvar,
                help=f"Database URL (or set {database_url_envvar} env var)",
            ),
        ] = None,
        echo: Annotated[
            bool, typer.Option("--echo", help="Echo SQL statements")
        ] = False,
    ):
        """Downgrade database to a specific revision."""
        logger.info(f"Downgrading to revision: {revision}")

        db_url = _get_database_url(database_url)
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
