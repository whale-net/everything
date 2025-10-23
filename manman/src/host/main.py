import logging
import logging.config
import os
import threading
from typing import Optional

import sqlalchemy
import typer
import uvicorn
from gunicorn.app.base import BaseApplication
from typing_extensions import Annotated

try:
    from importlib.resources import files
except ImportError:
    from importlib_resources import files

from libs.python.alembic import (
    create_alembic_config,
    create_migration as create_migration_util,
    run_downgrade as run_downgrade_util,
    run_migration as run_migration_util,
    should_run_migration,
)
from libs.python.cli.providers.rabbitmq import rmq_params
from libs.python.cli.providers.postgres import pg_params
from libs.python.cli.params import logging_params
from libs.python.cli.types import AppEnv
from libs.python.gunicorn import get_gunicorn_config
from manman.src.config import ManManConfig
from libs.python.rmq import ExchangeRegistry
from manman.src.util import get_sqlalchemy_engine, init_sql_alchemy_engine
from libs.python.rmq import (
    create_rabbitmq_vhost,
    init_rabbitmq_from_config,
)

app = typer.Typer()
logger = logging.getLogger(__name__)


# Helper function for exchange initialization
def initialize_rabbitmq_exchanges():
    """Declare RabbitMQ exchanges using the persistent connection."""
    from libs.python.rmq import get_rabbitmq_connection

    rmq_connection = get_rabbitmq_connection()

    exchanges = [exchange.value for exchange in ExchangeRegistry]
    for exchange in exchanges:
        rmq_connection.channel().exchange.declare(
            exchange=exchange,
            exchange_type="topic",
            durable=True,
        )
        logger.info("Exchange declared %s", exchange)


class GunicornApplication(BaseApplication):
    """Custom Gunicorn application that allows programmatic configuration."""

    def __init__(self, app_factory, options=None):
        self.options = options or {}
        self.app_factory = app_factory
        super().__init__()

    def load_config(self):
        """Load configuration from the options dict."""
        config = {
            key: value
            for key, value in self.options.items()
            if key in self.cfg.settings and value is not None
        }
        for key, value in config.items():
            self.cfg.set(key.lower(), value)

    def load(self):
        """Load the application."""
        return self.app_factory()




def create_experience_app():
    """Factory function to create the Experience API FastAPI application."""
    from manman.src.host.api.experience import create_app

    return create_app()


def create_status_app():
    """Factory function to create the Status API FastAPI application."""
    from manman.src.host.api.status import create_app

    return create_app()


def create_worker_dal_app():
    """Factory function to create the Worker DAL API FastAPI application."""
    from manman.src.host.api.worker_dal import create_app

    return create_app()



@app.command()
def start_experience_api(
    ctx: typer.Context,
    app_env: AppEnv = None,
    port: int = 8000,
    workers: Annotated[
        int, typer.Option(help="Number of Gunicorn worker processes")
    ] = 1,
    preload_app: Annotated[
        bool,
        typer.Option(
            help="Preload app before forking workers (recommended for multiple workers)"
        ),
    ] = True,
    should_run_migration_check: Optional[bool] = True,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the experience API (host layer) that provides game server management and user-facing functionality."""
    # Get contexts from decorators
    rmq_ctx = ctx.obj.get("rabbitmq", {})
    logging_ctx = ctx.obj.get("logging", {})
    log_otlp = logging_ctx.get("log_otlp", False)
    
    # Check migrations if needed
    if should_run_migration_check and _need_migration():
        raise RuntimeError("migration needs to be ran before starting")
    
    # Optionally create vhost
    if create_vhost and app_env == "dev":
        create_rabbitmq_vhost(
            host=rmq_ctx.get("host"),
            port=rmq_ctx.get("port"),
            username=rmq_ctx.get("username"),
            password=rmq_ctx.get("password"),
            vhost=rmq_ctx.get("vhost", "/"),
        )
    
    # Initialize RabbitMQ connection (reads vhost from provider config)
    init_rabbitmq_from_config(rmq_ctx)
    
    # Declare exchanges
    initialize_rabbitmq_exchanges()

    # Configure and run with Gunicorn
    options = get_gunicorn_config(
        microservice_name=ManManConfig.EXPERIENCE_API,
        port=port,
        workers=workers,
        enable_otel=log_otlp,
        preload_app=preload_app,
    )

    GunicornApplication(create_experience_app, options).run()



@app.command()
def start_status_api(
    ctx: typer.Context,
    app_env: AppEnv = None,
    port: int = 8000,
    workers: Annotated[
        int, typer.Option(help="Number of Gunicorn worker processes")
    ] = 1,
    preload_app: Annotated[
        bool,
        typer.Option(
            help="Preload app before forking workers (recommended for multiple workers)"
        ),
    ] = True,
    should_run_migration_check: Optional[bool] = True,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the status API that provides status and monitoring functionality."""
    # Get contexts from decorators
    rmq_ctx = ctx.obj.get("rabbitmq", {})
    logging_ctx = ctx.obj.get("logging", {})
    log_otlp = logging_ctx.get("log_otlp", False)
    
    # Check migrations if needed
    if should_run_migration_check and _need_migration():
        raise RuntimeError("migration needs to be ran before starting")
    
    # Optionally create vhost
    if create_vhost and app_env == "dev":
        create_rabbitmq_vhost(
            host=rmq_ctx.get("host"),
            port=rmq_ctx.get("port"),
            username=rmq_ctx.get("username"),
            password=rmq_ctx.get("password"),
            vhost=rmq_ctx.get("vhost", "/"),
        )
    
    # Initialize RabbitMQ connection (reads vhost from provider config)
    init_rabbitmq_from_config(rmq_ctx)
    
    # Declare exchanges
    initialize_rabbitmq_exchanges()

    # Configure and run with Gunicorn
    options = get_gunicorn_config(
        microservice_name=ManManConfig.STATUS_API,
        port=port,
        workers=workers,
        enable_otel=log_otlp,
        preload_app=preload_app,
    )

    GunicornApplication(create_status_app, options).run()



@app.command()
def start_worker_dal_api(
    ctx: typer.Context,
    app_env: AppEnv = None,
    port: int = 8000,
    workers: Annotated[
        int, typer.Option(help="Number of Gunicorn worker processes")
    ] = 1,
    preload_app: Annotated[
        bool,
        typer.Option(
            help="Preload app before forking workers (recommended for multiple workers)"
        ),
    ] = True,
    should_run_migration_check: Optional[bool] = True,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the worker DAL API that provides data access endpoints for worker services."""
    # Get contexts from decorators
    rmq_ctx = ctx.obj.get("rabbitmq", {})
    logging_ctx = ctx.obj.get("logging", {})
    log_otlp = logging_ctx.get("log_otlp", False)
    
    # Check migrations if needed
    if should_run_migration_check and _need_migration():
        raise RuntimeError("migration needs to be ran before starting")
    
    # Optionally create vhost
    if create_vhost and app_env == "dev":
        create_rabbitmq_vhost(
            host=rmq_ctx.get("host"),
            port=rmq_ctx.get("port"),
            username=rmq_ctx.get("username"),
            password=rmq_ctx.get("password"),
            vhost=rmq_ctx.get("vhost", "/"),
        )
    
    # Initialize RabbitMQ connection (reads vhost from provider config)
    init_rabbitmq_from_config(rmq_ctx)
    
    # Declare exchanges
    initialize_rabbitmq_exchanges()

    # Configure and run with Gunicorn
    options = get_gunicorn_config(
        microservice_name=ManManConfig.WORKER_DAL_API,
        port=port,
        workers=workers,
        enable_otel=log_otlp,
        preload_app=preload_app,
    )

    GunicornApplication(create_worker_dal_app, options).run()



@app.command()
def start_status_processor(
    ctx: typer.Context,
    app_env: AppEnv = None,
    should_run_migration_check: Optional[bool] = True,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the status event processor that handles status-related pub/sub messages."""
    # Get contexts from decorators
    rmq_ctx = ctx.obj.get("rabbitmq", {})
    logging_ctx = ctx.obj.get("logging", {})
    log_otlp = logging_ctx.get("log_otlp", False)

    logger.info("Starting status event processor...")

    # Check migrations if needed
    if should_run_migration_check and _need_migration():
        raise RuntimeError("migration needs to be ran before starting")
    
    # Optionally create vhost
    if create_vhost and app_env == "dev":
        create_rabbitmq_vhost(
            host=rmq_ctx.get("host"),
            port=rmq_ctx.get("port"),
            username=rmq_ctx.get("username"),
            password=rmq_ctx.get("password"),
            vhost=rmq_ctx.get("vhost", "/"),
        )
    
    # Initialize RabbitMQ connection (reads vhost from provider config)
    init_rabbitmq_from_config(rmq_ctx)
    
    # Declare exchanges
    initialize_rabbitmq_exchanges()

    # Start the status event processor (pub/sub only, no HTTP server other than health check)
    from fastapi import FastAPI

    from manman.src.host.api.shared import add_health_check
    from manman.src.host.status_processor import StatusEventProcessor
    from libs.python.rmq import get_rabbitmq_connection

    # Define and run health check API in a separate thread
    health_check_app = FastAPI(title="ManMan Status Processor Health Check")
    add_health_check(health_check_app)

    def run_health_check_server():
        # Health check server uses same logging as parent process
        uvicorn.run(
            health_check_app,
            host="0.0.0.0",
            port=8000,
            # Disable uvicorn's log configuration since we handle it ourselves
            log_config=None,
        )

    health_check_thread = threading.Thread(target=run_health_check_server, daemon=True)
    health_check_thread.start()

    logger.info("Health check API for status processor started on port 8000")

    processor = StatusEventProcessor(get_rabbitmq_connection())
    processor.run()



@app.command()
@logging_params  # Auto-configures logging from environment variables
def run_migration(ctx: typer.Context):
    _run_migration(get_sqlalchemy_engine())


@app.command()
@logging_params  # Auto-configures logging from environment variables
def create_migration(ctx: typer.Context, migration_message: Optional[str] = None):
    # TODO - make use of this? or remove
    if os.environ.get("ENVIRONMENT", "DEV") == "PROD":
        raise RuntimeError("cannot create revisions in production")
    _create_migration(get_sqlalchemy_engine(), message=migration_message)


@app.command()
@logging_params  # Auto-configures logging from environment variables
def run_downgrade(ctx: typer.Context, target: str):
    config = _get_alembic_config()
    engine = get_sqlalchemy_engine()
    run_downgrade_util(engine, config, target)


@app.callback()
@rmq_params
@logging_params  # Auto-configures logging from environment variables
@pg_params
def callback(ctx: typer.Context, app_env: AppEnv = None):
    # Initialize database connection for CLI operations
    init_sql_alchemy_engine(ctx.obj.get("postgres")["database_url"])
    
    # Logging is already configured by @logging_params decorator
    # No need to call configure_logging() here!
    # Config read from: APP_NAME, APP_DOMAIN, APP_TYPE, APP_VERSION, LOG_LEVEL, LOG_OTLP, etc.


# alembic helpers using consolidated library
def _get_alembic_config():
    """Get Alembic configuration using consolidated library."""
    migrations_dir = str(files("manman.src.migrations"))
    db_url = os.environ.get("POSTGRES_URL", "")
    
    if not db_url:
        raise RuntimeError("Required environment variable 'POSTGRES_URL' is not set. Please set 'POSTGRES_URL' to your database connection string.")
    
    return create_alembic_config(
        migrations_dir=migrations_dir,
        database_url=db_url,
        version_table_schema="public",
    )


def _need_migration() -> bool:
    """Check if migrations are needed."""
    config = _get_alembic_config()
    engine = get_sqlalchemy_engine()
    return should_run_migration(engine, config)


def _run_migration(engine: sqlalchemy.Engine):
    """Run migrations using consolidated library."""
    config = _get_alembic_config()
    run_migration_util(engine, config)


def _create_migration(engine: sqlalchemy.Engine, message: Optional[str] = None):
    """Create a new migration using consolidated library."""
    config = _get_alembic_config()
    create_migration_util(engine, config, message)


if __name__ == "__main__":
    app()
