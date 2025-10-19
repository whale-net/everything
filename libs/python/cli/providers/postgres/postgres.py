"""PostgreSQL database provider with Alembic migration support.

Provides a reusable database context factory for PostgreSQL connections
with integrated Alembic configuration.

Example:
    ```python
    from libs.python.cli.providers.postgres import pg_params

    app = typer.Typer()

    @app.callback()
    @pg_params
    def setup(ctx: typer.Context):
        pg = ctx.obj['postgres']
        # pg = {'database_url': '...'}
    ```
"""

import inspect
import logging
from dataclasses import dataclass
from typing import Annotated, Callable, Optional

import alembic.config
import typer
from sqlalchemy import Engine, create_engine

try:
    from importlib.resources import files
except ImportError:
    # Python < 3.9 fallback
    from importlib_resources import files

from libs.python.alembic import create_alembic_config

logger = logging.getLogger(__name__)


# Type alias for CLI parameters - preserves Typer hints and envvar support
# Note: Uses POSTGRES_URL for backwards compatibility with existing apps
PostgresUrl = Annotated[str, typer.Option(..., envvar="POSTGRES_URL")]


@dataclass
class DatabaseContext:
    """Typed database context with engine and migration config.
    
    Attributes:
        engine: SQLAlchemy engine for database connections
        alembic_config: Alembic configuration for migrations
        url: Original database URL
        migrations_package: Python package containing migrations
    """

    engine: Engine
    alembic_config: alembic.config.Config
    url: str
    migrations_package: str


def create_postgres_context(
    database_url: PostgresUrl,
    migrations_package: str,
    echo: bool = False,
    pool_pre_ping: bool = True,
    pool_recycle: int = 60,
    version_table_schema: str = "public",
    engine_initializer: Optional[Callable[[Engine], None]] = None,
) -> DatabaseContext:
    """Create PostgreSQL database context with Alembic support.
    
    Args:
        database_url: PostgreSQL connection URL (from CLI parameter)
        migrations_package: Python package path to migrations directory
        echo: Enable SQL query logging
        pool_pre_ping: Enable connection health checks
        pool_recycle: Recycle connections after N seconds
        version_table_schema: Schema for alembic_version table
        engine_initializer: Optional function to initialize engine (e.g., register event listeners)
    
    Returns:
        DatabaseContext with configured engine and Alembic config
        
    Example:
        >>> ctx = create_postgres_context(
        ...     database_url="postgresql://user:pass@localhost/db",
        ...     migrations_package="myapp.migrations",
        ... )
        >>> ctx.engine.connect()
    """
    logger.debug("Creating PostgreSQL context for package: %s", migrations_package)

    # Create engine with standard production settings
    engine = create_engine(
        url=database_url,
        echo=echo,
        pool_pre_ping=pool_pre_ping,
        pool_recycle=pool_recycle,
    )

    # Run custom engine initialization if provided
    if engine_initializer:
        logger.debug("Running custom engine initializer")
        engine_initializer(engine)

    # Get migrations directory from package
    migrations_dir = str(files(migrations_package))
    logger.debug("Migrations directory: %s", migrations_dir)

    # Create Alembic configuration
    alembic_cfg = create_alembic_config(
        migrations_dir=migrations_dir,
        database_url=database_url,
        version_table_schema=version_table_schema,
    )

    logger.debug("PostgreSQL context created successfully")

    return DatabaseContext(
        engine=engine,
        alembic_config=alembic_cfg,
        url=database_url,
        migrations_package=migrations_package,
    )


# ==============================================================================
# Decorator for injecting PostgreSQL parameters
# ==============================================================================

def pg_params(func: Callable) -> Callable:
    """
    Decorator that injects PostgreSQL parameters into the callback.
    
    Usage:
        @app.callback()
        @pg_params
        def callback(ctx: typer.Context, ...):
            pg = ctx.obj['postgres']
            # pg = {'database_url': '...'}
    """
    from libs.python.cli.params_base import _create_param_decorator
    
    param_specs = [
        ('database_url', inspect.Parameter(
            'database_url', inspect.Parameter.KEYWORD_ONLY,
            annotation=PostgresUrl
        )),
    ]
    
    def extractor(kwargs):
        return {
            'database_url': kwargs.pop('database_url'),
        }
    
    return _create_param_decorator(param_specs, 'postgres', extractor)(func)
