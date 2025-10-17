import logging
import logging.config
import os
import threading
from typing import Optional

import alembic
import alembic.command
import alembic.config
import sqlalchemy
import typer
import uvicorn
from gunicorn.app.base import BaseApplication
from typing_extensions import Annotated

from manman.src.config import ManManConfig
from manman.src.logging_config import (
    get_gunicorn_config,
    setup_logging,
    setup_server_logging,
)
from libs.python.rmq import ExchangeRegistry
from manman.src.util import get_sqlalchemy_engine, init_sql_alchemy_engine
from libs.python.rmq import (
    create_rabbitmq_vhost,
    get_rabbitmq_ssl_options,
    init_rabbitmq,
)

app = typer.Typer()
logger = logging.getLogger(__name__)


# Global configuration store for initialization parameters
_initialization_config = {}
_initialization_lock = threading.Lock()
_services_initialized = False


def store_initialization_config(**kwargs):
    """Store initialization configuration globally for use in app factories."""
    global _initialization_config
    with _initialization_lock:
        _initialization_config.update(kwargs)


def get_initialization_config():
    """Get stored initialization configuration."""
    with _initialization_lock:
        return _initialization_config.copy()


def ensure_common_services_initialized():
    """
    Ensure common services are initialized using stored configuration, thread-safe.

    This function handles several race condition scenarios:
    1. Multiple Gunicorn workers starting simultaneously (preload_app=False)
    2. App factory called during preload and again in workers (preload_app=True)
    3. Concurrent access to global configuration state

    Uses a threading lock and initialization flag to ensure services are
    initialized exactly once, regardless of how many times this is called.
    """
    global _services_initialized

    with _initialization_lock:
        if _services_initialized:
            logger.debug("Common services already initialized, skipping")
            return

        config = _initialization_config.copy()
        if not config:
            logger.warning(
                "No initialization configuration found - services may not be properly initialized"
            )
            return

        try:
            _init_common_services(**config)
            _services_initialized = True
            logger.info("Common services initialized successfully")
        except Exception as e:
            logger.error("Failed to initialize common services: %s", e)
            raise


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


def _init_common_services(
    rabbitmq_host: str,
    rabbitmq_port: int,
    rabbitmq_username: str,
    rabbitmq_password: str,
    app_env: Optional[str],
    enable_ssl: bool,
    rabbitmq_ssl_hostname: Optional[str],
    should_run_migration_check: bool,
    create_vhost: bool = False,
    enable_otel: bool = False,  # Add enable_otel parameter
):
    """Initialize common services required by both APIs."""
    if should_run_migration_check and _need_migration():
        raise RuntimeError("migration needs to be ran before starting")
    virtual_host = f"manman-{app_env}" if app_env else "/"
    # Optionally create vhost via management API
    if create_vhost and app_env == "dev":
        create_rabbitmq_vhost(
            host=rabbitmq_host,
            port=rabbitmq_port,
            username=rabbitmq_username,
            password=rabbitmq_password,
            vhost=virtual_host,
        )

    # Initialize with AMQPStorm connection parameters
    init_rabbitmq(
        host=rabbitmq_host,
        port=rabbitmq_port,
        username=rabbitmq_username,
        password=rabbitmq_password,
        virtual_host=virtual_host,
        ssl_enabled=enable_ssl,
        ssl_options=get_rabbitmq_ssl_options(
            hostname=rabbitmq_ssl_hostname,
        )
        if enable_ssl
        else None,
    )

    # declare rabbitmq exchanges - use persistent connection for this operation
    from libs.python.rmq import get_rabbitmq_connection

    rmq_connection = get_rabbitmq_connection()

    exchanges = []
    for exchange in ExchangeRegistry:
        exchanges.append(exchange.value)
    for exchange in exchanges:
        rmq_connection.channel().exchange.declare(
            exchange=exchange,
            exchange_type="topic",
            durable=True,
        )
        logger.info("Exchange declared %s", exchange)


def create_experience_app():
    """Factory function to create the Experience API FastAPI application with service initialization."""
    # Ensure services are initialized when creating the app
    ensure_common_services_initialized()

    # Configure server-specific logging using Python objects
    setup_server_logging(ManManConfig.EXPERIENCE_API)

    from manman.src.host.api.experience import create_app

    return create_app()


def create_status_app():
    """Factory function to create the Status API FastAPI application with service initialization."""
    # Ensure services are initialized when creating the app
    ensure_common_services_initialized()

    # Configure server-specific logging using Python objects
    setup_server_logging(ManManConfig.STATUS_API)

    from manman.src.host.api.status import create_app

    return create_app()


def create_worker_dal_app():
    """Factory function to create the Worker DAL API FastAPI application with service initialization."""
    # Ensure services are initialized when creating the app
    ensure_common_services_initialized()

    # Configure server-specific logging using Python objects
    setup_server_logging(ManManConfig.WORKER_DAL_API)

    from manman.src.host.api.worker_dal import create_app

    return create_app()


@app.command()
def start_experience_api(
    rabbitmq_host: Annotated[str, typer.Option(envvar="RABBITMQ_HOST")],
    rabbitmq_port: Annotated[int, typer.Option(envvar="RABBITMQ_PORT")],
    rabbitmq_username: Annotated[str, typer.Option(envvar="RABBITMQ_USER")],
    rabbitmq_password: Annotated[str, typer.Option(envvar="RABBITMQ_PASSWORD")],
    app_env: Annotated[Optional[str], typer.Option(envvar="APP_ENV")] = None,
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
    enable_ssl: Annotated[
        bool, typer.Option(envvar="RABBITMQ_ENABLE_SSL")
    ] = False,
    rabbitmq_ssl_hostname: Annotated[
        str, typer.Option(envvar="RABBITMQ_SSL_HOSTNAME")
    ] = None,
    log_otlp: Annotated[
        bool,
        typer.Option(
            envvar="LOG_OTLP", help="Enable OpenTelemetry OTLP logging"
        ),
    ] = False,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the experience API (host layer) that provides game server management and user-facing functionality."""
    # Setup logging first
    setup_logging(
        microservice_name=ManManConfig.EXPERIENCE_API,
        app_env=app_env,
        enable_otel=log_otlp,
    )

    # Store initialization configuration for use in app factory
    store_initialization_config(
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        rabbitmq_username=rabbitmq_username,
        rabbitmq_password=rabbitmq_password,
        app_env=app_env,
        enable_ssl=enable_ssl,
        rabbitmq_ssl_hostname=rabbitmq_ssl_hostname,
        should_run_migration_check=should_run_migration_check,
        create_vhost=create_vhost,
        enable_otel=log_otlp,  # Store OTEL flag for app factory
    )

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
    rabbitmq_host: Annotated[str, typer.Option(envvar="RABBITMQ_HOST")],
    rabbitmq_port: Annotated[int, typer.Option(envvar="RABBITMQ_PORT")],
    rabbitmq_username: Annotated[str, typer.Option(envvar="RABBITMQ_USER")],
    rabbitmq_password: Annotated[str, typer.Option(envvar="RABBITMQ_PASSWORD")],
    app_env: Annotated[Optional[str], typer.Option(envvar="APP_ENV")] = None,
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
    enable_ssl: Annotated[
        bool, typer.Option(envvar="RABBITMQ_ENABLE_SSL")
    ] = False,
    rabbitmq_ssl_hostname: Annotated[
        str, typer.Option(envvar="RABBITMQ_SSL_HOSTNAME")
    ] = None,
    log_otlp: Annotated[
        bool,
        typer.Option(
            envvar="LOG_OTLP", help="Enable OpenTelemetry OTLP logging"
        ),
    ] = False,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the status API that provides status and monitoring functionality."""
    # Setup logging first
    setup_logging(
        microservice_name=ManManConfig.STATUS_API, app_env=app_env, enable_otel=log_otlp
    )

    # Store initialization configuration for use in app factory
    store_initialization_config(
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        rabbitmq_username=rabbitmq_username,
        rabbitmq_password=rabbitmq_password,
        app_env=app_env,
        enable_ssl=enable_ssl,
        rabbitmq_ssl_hostname=rabbitmq_ssl_hostname,
        should_run_migration_check=should_run_migration_check,
        create_vhost=create_vhost,
        enable_otel=log_otlp,  # Store OTEL flag for app factory
    )

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
    rabbitmq_host: Annotated[str, typer.Option(envvar="RABBITMQ_HOST")],
    rabbitmq_port: Annotated[int, typer.Option(envvar="RABBITMQ_PORT")],
    rabbitmq_username: Annotated[str, typer.Option(envvar="RABBITMQ_USER")],
    rabbitmq_password: Annotated[str, typer.Option(envvar="RABBITMQ_PASSWORD")],
    app_env: Annotated[Optional[str], typer.Option(envvar="APP_ENV")] = None,
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
    enable_ssl: Annotated[
        bool, typer.Option(envvar="RABBITMQ_ENABLE_SSL")
    ] = False,
    rabbitmq_ssl_hostname: Annotated[
        str, typer.Option(envvar="RABBITMQ_SSL_HOSTNAME")
    ] = None,
    log_otlp: Annotated[
        bool,
        typer.Option(
            envvar="LOG_OTLP", help="Enable OpenTelemetry OTLP logging"
        ),
    ] = False,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the worker DAL API that provides data access endpoints for worker services."""
    # Setup logging first
    setup_logging(
        microservice_name=ManManConfig.WORKER_DAL_API,
        app_env=app_env,
        enable_otel=log_otlp,
    )

    # Store initialization configuration for use in app factory
    store_initialization_config(
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        rabbitmq_username=rabbitmq_username,
        rabbitmq_password=rabbitmq_password,
        app_env=app_env,
        enable_ssl=enable_ssl,
        rabbitmq_ssl_hostname=rabbitmq_ssl_hostname,
        should_run_migration_check=should_run_migration_check,
        create_vhost=create_vhost,
        enable_otel=log_otlp,  # Store OTEL flag for app factory
    )

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
    rabbitmq_host: Annotated[str, typer.Option(envvar="RABBITMQ_HOST")],
    rabbitmq_port: Annotated[int, typer.Option(envvar="RABBITMQ_PORT")],
    rabbitmq_username: Annotated[str, typer.Option(envvar="RABBITMQ_USER")],
    rabbitmq_password: Annotated[str, typer.Option(envvar="RABBITMQ_PASSWORD")],
    app_env: Annotated[Optional[str], typer.Option(envvar="APP_ENV")] = None,
    should_run_migration_check: Optional[bool] = True,
    enable_ssl: Annotated[
        bool, typer.Option(envvar="RABBITMQ_ENABLE_SSL")
    ] = False,
    rabbitmq_ssl_hostname: Annotated[
        str, typer.Option(envvar="RABBITMQ_SSL_HOSTNAME")
    ] = None,
    log_otlp: Annotated[
        bool,
        typer.Option(
            envvar="LOG_OTLP", help="Enable OpenTelemetry OTLP logging"
        ),
    ] = False,
    create_vhost: Annotated[
        bool, typer.Option(help="Create RabbitMQ vhost before initialization")
    ] = False,
):
    """Start the status event processor that handles status-related pub/sub messages."""

    # Setup logging first - this is a standalone service (no uvicorn)
    setup_logging(
        microservice_name=ManManConfig.STATUS_PROCESSOR,
        app_env=app_env,
        enable_otel=log_otlp,
    )

    logger.info("Starting status event processor...")

    store_initialization_config(
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        rabbitmq_username=rabbitmq_username,
        rabbitmq_password=rabbitmq_password,
        app_env=app_env,
        enable_ssl=enable_ssl,
        rabbitmq_ssl_hostname=rabbitmq_ssl_hostname,
        should_run_migration_check=should_run_migration_check,
        create_vhost=create_vhost,
        enable_otel=log_otlp,  # Store OTEL flag for status processor
    )

    ensure_common_services_initialized()

    # Start the status event processor (pub/sub only, no HTTP server other than health check)
    from fastapi import FastAPI  # Add FastAPI import

    from manman.src.host.api.shared import (
        add_health_check,  # Ensure this import is present or add it
    )
    from manman.src.host.status_processor import StatusEventProcessor
    from libs.python.rmq import get_rabbitmq_connection

    # Define and run health check API in a separate thread
    health_check_app = FastAPI(title="ManMan Status Processor Health Check")
    add_health_check(health_check_app)

    def run_health_check_server():
        # Use our uvicorn config for the health check server too
        # Configure server logging directly using Python objects
        setup_server_logging("status-processor")

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
def run_migration():
    setup_logging()  # Basic logging for CLI operations
    _run_migration(get_sqlalchemy_engine())


@app.command()
def create_migration(migration_message: Optional[str] = None):
    setup_logging()  # Basic logging for CLI operations
    # TODO - make use of this? or remove
    if os.environ.get("ENVIRONMENT", "DEV") == "PROD":
        raise RuntimeError("cannot create revisions in production")
    _create_migration(get_sqlalchemy_engine(), message=migration_message)


@app.command()
def run_downgrade(target: str):
    setup_logging()  # Basic logging for CLI operations
    config = _get_alembic_config()
    engine = get_sqlalchemy_engine()
    with engine.begin() as conn:
        config.attributes["connection"] = conn
        alembic.command.downgrade(config, target)


@app.callback()
def callback(
    db_connection_string: Annotated[str, typer.Option(envvar="POSTGRES_URL")],
):
    # Initialize database connection for CLI operations
    # Note: Logging will be configured by individual commands as needed
    init_sql_alchemy_engine(db_connection_string)


# alembic helpers
# TODO context manager to reduce duplication
# TODO - figure out what I meant with the previous TODO
def _get_alembic_config() -> alembic.config.Config:
    # Configure Alembic programmatically without requiring alembic.ini
    # This is necessary for containerized environments where the ini file may not exist
    # Pass file_=None to indicate we're configuring programmatically
    config = alembic.config.Config(file_=None, ini_section="alembic")
    
    # Find the migrations directory using Python's module system
    # The migrations are packaged as manman.src.migrations
    import manman.src.migrations
    migrations_dir = os.path.dirname(manman.src.migrations.__file__)
    
    # Set the script location - this is required by Alembic
    config.set_main_option("script_location", migrations_dir)
    
    # Set the database URL from environment (used by migrations in offline mode)
    # In online mode, env.py gets the URL from environment directly
    db_url = os.environ.get("POSTGRES_URL", "")
    if db_url:
        config.set_main_option("sqlalchemy.url", db_url)
    
    return config


def _need_migration() -> bool:
    config = _get_alembic_config()
    engine = get_sqlalchemy_engine()
    with engine.begin() as conn:
        # TODO remove connection from env and use this?
        config.attributes["connection"] = conn
        try:
            alembic.command.check(config)
        except alembic.command.util.AutogenerateDiffsDetected:
            return True

        return False


def _run_migration(engine: sqlalchemy.Engine):
    config = _get_alembic_config()
    # with engine.begin() as conn:
    # config.attributes["connection"] = conn
    alembic.command.upgrade(config, "head")


def _create_migration(engine: sqlalchemy.Engine, message: Optional[str] = None):
    if not _need_migration():
        raise RuntimeError("no migration creation required")

    config = _get_alembic_config()
    # with engine.begin() as conn:
    # config.attributes["connection"] = conn
    alembic.command.revision(config, message=message, autogenerate=True)


if __name__ == "__main__":
    app()
