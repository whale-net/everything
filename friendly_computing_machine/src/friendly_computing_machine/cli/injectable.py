"""
Injectable dependencies for CLI commands.

This module provides injectable versions of common dependencies like database
connections, API clients, and configuration. These can be used with the Depends
annotation to enable automatic dependency injection.

Example:
    ```python
    from friendly_computing_machine.cli.injectable import get_db_context
    from libs.python.cli.deps import Depends
    
    @app.command()
    def my_command(
        ctx: typer.Context,
        db_ctx: Annotated[DBContext, Depends(get_db_context)],
    ):
        # db_ctx is automatically injected
        engine = db_ctx.engine
        config = db_ctx.alembic_config
    ```
"""

import logging
from typing import Annotated

import typer

from friendly_computing_machine.src.friendly_computing_machine.cli.context.app_env import (
    T_app_env,
    setup_app_env,
    FILENAME as APP_ENV_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.db import (
    DBContext,
    T_database_url,
    setup_db,
    FILENAME as DB_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.gemini import (
    T_google_api_key,
    setup_gemini,
    FILENAME as GEMINI_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.log import (
    setup_logging,
    FILENAME as LOG_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.manman_host import (
    T_manman_host_url,
    setup_manman_experience_api,
    setup_manman_status_api,
    FILENAME as MANMAN_FILENAME,
    SupportedAPI,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.rabbitmq import (
    T_rabbitmq_enable_ssl,
    T_rabbitmq_host,
    T_rabbitmq_password,
    T_rabbitmq_port,
    T_rabbitmq_ssl_hostname,
    T_rabbitmq_user,
    T_rabbitmq_vhost,
    setup_rabbitmq,
    FILENAME as RABBITMQ_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.slack import (
    T_slack_app_token,
    T_slack_bot_token,
    setup_slack,
    setup_slack_web_client_only,
    FILENAME as SLACK_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.temporal import (
    T_temporal_host,
    TemporalConfig,
    setup_temporal,
    FILENAME as TEMPORAL_FILENAME,
)
from libs.python.cli.deps import (
    Depends,
    injectable,
)

logger = logging.getLogger(__name__)


@injectable
def get_logging_config(
    ctx: typer.Context,
    log_otlp: bool = False,
    log_console: bool = False,
) -> dict:
    """
    Get logging configuration.
    
    Args:
        ctx: Typer context
        log_otlp: Enable OTLP logging
        log_console: Enable console logging
        
    Returns:
        Logging configuration dictionary
    """
    setup_logging(ctx, log_otlp=log_otlp, log_console=log_console)
    return ctx.obj[LOG_FILENAME]


@injectable
def get_app_env(
    ctx: typer.Context,
    app_env: T_app_env,
) -> str:
    """
    Get application environment.
    
    Args:
        ctx: Typer context
        app_env: Application environment (dev, staging, prod)
        
    Returns:
        The application environment string
    """
    setup_app_env(ctx, app_env)
    return ctx.obj[APP_ENV_FILENAME]["app_env"]


@injectable
def get_db_context(
    ctx: typer.Context,
    database_url: T_database_url,
    echo: bool = False,
) -> DBContext:
    """
    Get database context with engine and Alembic configuration.
    
    Args:
        ctx: Typer context
        database_url: Database connection URL
        echo: Enable SQLAlchemy echo mode
        
    Returns:
        DBContext with engine and alembic_config
    """
    setup_db(ctx, database_url, echo=echo)
    return ctx.obj[DB_FILENAME]


@injectable
def get_slack_tokens(
    ctx: typer.Context,
    slack_app_token: T_slack_app_token,
    slack_bot_token: T_slack_bot_token,
) -> dict:
    """
    Get Slack tokens for both Socket Mode and Web API.
    
    Args:
        ctx: Typer context
        slack_app_token: Slack app token for Socket Mode
        slack_bot_token: Slack bot token for Web API
        
    Returns:
        Dictionary with slack_app_token and slack_bot_token
    """
    setup_slack(ctx, slack_app_token, slack_bot_token)
    return ctx.obj[SLACK_FILENAME]


@injectable
def get_slack_bot_token(
    ctx: typer.Context,
    slack_bot_token: T_slack_bot_token,
) -> dict:
    """
    Get Slack bot token for Web API only.
    
    Args:
        ctx: Typer context
        slack_bot_token: Slack bot token for Web API
        
    Returns:
        Dictionary with slack_bot_token
    """
    setup_slack_web_client_only(ctx, slack_bot_token)
    return ctx.obj[SLACK_FILENAME]


@injectable
def get_temporal_config(
    ctx: typer.Context,
    temporal_host: T_temporal_host,
    app_env: Annotated[str, Depends(get_app_env)],
) -> TemporalConfig:
    """
    Get Temporal client configuration.
    
    This demonstrates dependency chaining - it depends on get_app_env.
    
    Args:
        ctx: Typer context
        temporal_host: Temporal server host
        app_env: Application environment (automatically injected)
        
    Returns:
        TemporalConfig with host information
    """
    # Note: app_env is already resolved by get_app_env dependency
    # so we can pass it directly to setup_temporal
    setup_temporal(ctx, temporal_host, ctx.obj[APP_ENV_FILENAME]["app_env"])
    return ctx.obj[TEMPORAL_FILENAME]


@injectable
def get_manman_experience_api(
    ctx: typer.Context,
    manman_host_url: T_manman_host_url,
) -> type:
    """
    Get ManMan Experience API client.
    
    Args:
        ctx: Typer context
        manman_host_url: ManMan host URL
        
    Returns:
        ManManExperienceAPI class
    """
    setup_manman_experience_api(ctx, manman_host_url)
    return ctx.obj[MANMAN_FILENAME][SupportedAPI.experience]


@injectable
def get_manman_status_api(
    ctx: typer.Context,
    manman_host_url: T_manman_host_url,
) -> type:
    """
    Get ManMan Status API client.
    
    Args:
        ctx: Typer context
        manman_host_url: ManMan host URL
        
    Returns:
        ManManStatusAPI class
    """
    setup_manman_status_api(ctx, manman_host_url)
    return ctx.obj[MANMAN_FILENAME][SupportedAPI.status]


@injectable
def get_rabbitmq_config(
    ctx: typer.Context,
    rabbitmq_host: T_rabbitmq_host,
    rabbitmq_port: T_rabbitmq_port = 5672,
    rabbitmq_user: T_rabbitmq_user = None,
    rabbitmq_password: T_rabbitmq_password = None,
    rabbitmq_enable_ssl: T_rabbitmq_enable_ssl = False,
    rabbitmq_ssl_hostname: T_rabbitmq_ssl_hostname = None,
    rabbitmq_vhost: T_rabbitmq_vhost = "/",
) -> dict:
    """
    Get RabbitMQ configuration.
    
    Args:
        ctx: Typer context
        rabbitmq_host: RabbitMQ host
        rabbitmq_port: RabbitMQ port
        rabbitmq_user: RabbitMQ username
        rabbitmq_password: RabbitMQ password
        rabbitmq_enable_ssl: Enable SSL
        rabbitmq_ssl_hostname: SSL hostname
        rabbitmq_vhost: Virtual host
        
    Returns:
        Dictionary with RabbitMQ configuration
    """
    setup_rabbitmq(
        ctx,
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        rabbitmq_user=rabbitmq_user,
        rabbitmq_password=rabbitmq_password,
        rabbitmq_enable_ssl=rabbitmq_enable_ssl,
        rabbitmq_ssl_hostname=rabbitmq_ssl_hostname,
        rabbitmq_vhost=rabbitmq_vhost,
    )
    return ctx.obj[RABBITMQ_FILENAME]


@injectable
def get_gemini_config(
    ctx: typer.Context,
    google_api_key: T_google_api_key,
) -> bool:
    """
    Get Gemini API configuration.
    
    Args:
        ctx: Typer context
        google_api_key: Google API key for Gemini
        
    Returns:
        True if configured
    """
    setup_gemini(ctx, google_api_key)
    return ctx.obj[GEMINI_FILENAME]
