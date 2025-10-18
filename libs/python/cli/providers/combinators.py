"""Provider combinators for composing CLI contexts with automatic initialization.

This module provides high-level functions that combine multiple providers
and handle initialization automatically, reducing boilerplate in CLI code.

Example:
    ```python
    from libs.python.cli.providers.combinators import setup_postgres_with_fcm_init
    
    @app.callback()
    def callback(ctx: typer.Context, database_url: PostgresUrl):
        # Automatic engine initialization, no callbacks needed
        ctx.obj = setup_postgres_with_fcm_init(
            database_url=database_url,
            migrations_package="myapp.migrations",
        )
    ```
"""

import logging
from typing import Callable, Optional

from libs.python.cli.providers.postgres import DatabaseContext, PostgresUrl, create_postgres_context
from libs.python.cli.providers.rabbitmq import (
    RabbitMQContext,
    RabbitMQHost,
    RabbitMQPort,
    RabbitMQUser,
    RabbitMQPassword,
    RabbitMQVHost,
    RabbitMQEnableSSL,
    RabbitMQSSLHostname,
    create_rabbitmq_context,
)
from libs.python.cli.providers.slack import SlackBotToken, SlackAppToken, SlackContext, create_slack_context

logger = logging.getLogger(__name__)


def setup_postgres_with_fcm_init(
    database_url: PostgresUrl,
    migrations_package: str = "friendly_computing_machine.src.migrations",
    **kwargs,
) -> DatabaseContext:
    """Create PostgreSQL context with FCM-specific engine initialization.
    
    Automatically imports and applies FCM's init_engine function.
    
    Args:
        database_url: PostgreSQL connection URL
        migrations_package: Python package containing migrations
        **kwargs: Additional arguments passed to create_postgres_context
    
    Returns:
        DatabaseContext with FCM engine initialization applied
    """
    from friendly_computing_machine.src.friendly_computing_machine.db.util import init_engine
    
    return create_postgres_context(
        database_url=database_url,
        migrations_package=migrations_package,
        engine_initializer=init_engine,
        **kwargs,
    )


def setup_slack_with_fcm_init(
    bot_token: SlackBotToken,
    app_token: Optional[SlackAppToken] = None,
    **kwargs,
) -> SlackContext:
    """Create Slack context with FCM-specific web client initialization.
    
    Automatically imports and applies FCM's init_web_client function.
    
    Args:
        bot_token: Slack Bot Token
        app_token: Optional Slack App Token for Socket Mode
        **kwargs: Additional arguments passed to create_slack_context
    
    Returns:
        SlackContext with FCM web client initialization applied
    """
    from friendly_computing_machine.src.friendly_computing_machine.bot.app import init_web_client
    
    return create_slack_context(
        bot_token=bot_token,
        app_token=app_token,
        web_client_initializer=init_web_client,
        **kwargs,
    )


def setup_rabbitmq_with_fcm_init(
    host: RabbitMQHost,
    port: RabbitMQPort = 5672,
    user: RabbitMQUser = None,
    password: RabbitMQPassword = None,
    vhost: RabbitMQVHost = "/",
    enable_ssl: RabbitMQEnableSSL = False,
    ssl_hostname: RabbitMQSSLHostname = None,
    **kwargs,
) -> RabbitMQContext:
    """Create RabbitMQ context with FCM-specific initialization.
    
    Automatically imports and applies FCM's init_rabbitmq function.
    
    Args:
        host: RabbitMQ host
        port: RabbitMQ port
        user: Optional username
        password: Optional password
        vhost: Virtual host
        enable_ssl: Enable SSL connections
        ssl_hostname: SSL hostname for verification
        **kwargs: Additional arguments passed to create_rabbitmq_context
    
    Returns:
        RabbitMQContext with FCM RabbitMQ initialization applied
    """
    def init_rabbitmq(rmq_ctx: RabbitMQContext):
        from friendly_computing_machine.src.friendly_computing_machine.rabbitmq.util import (
            init_rabbitmq as fcm_init_rabbitmq,
        )
        fcm_init_rabbitmq(
            rabbitmq_host=rmq_ctx.host,
            rabbitmq_port=rmq_ctx.port,
            rabbitmq_user=rmq_ctx.user,
            rabbitmq_password=rmq_ctx.password,
            rabbitmq_enable_ssl=rmq_ctx.enable_ssl,
            rabbitmq_ssl_hostname=rmq_ctx.ssl_hostname,
            rabbitmq_vhost=rmq_ctx.vhost,
        )
    
    return create_rabbitmq_context(
        host=host,
        port=port,
        user=user,
        password=password,
        vhost=vhost,
        enable_ssl=enable_ssl,
        ssl_hostname=ssl_hostname,
        rabbitmq_initializer=init_rabbitmq,
        **kwargs,
    )


# Project-agnostic combinators (for other projects like manman)

def create_postgres_provider_factory(
    migrations_package: str,
    engine_initializer: Optional[Callable] = None,
) -> Callable[[PostgresUrl], DatabaseContext]:
    """Create a reusable Postgres provider factory for a specific project.
    
    This is useful when you want to create a project-specific provider that can
    be imported and used across multiple CLI modules.
    
    Args:
        migrations_package: Python package containing migrations
        engine_initializer: Optional function to initialize engine
    
    Returns:
        Factory function that creates DatabaseContext from database URL
        
    Example:
        ```python
        # In myapp/cli/providers.py
        from libs.python.cli.providers.combinators import create_postgres_provider_factory
        from myapp.db.util import init_engine
        
        setup_postgres = create_postgres_provider_factory(
            migrations_package="myapp.migrations",
            engine_initializer=init_engine,
        )
        
        # In myapp/cli/commands.py
        from myapp.cli.providers import setup_postgres
        
        @app.callback()
        def callback(ctx: typer.Context, database_url: PostgresUrl):
            ctx.obj = setup_postgres(database_url)
        ```
    """
    def factory(database_url: PostgresUrl, **kwargs) -> DatabaseContext:
        return create_postgres_context(
            database_url=database_url,
            migrations_package=migrations_package,
            engine_initializer=engine_initializer,
            **kwargs,
        )
    return factory


def create_slack_provider_factory(
    web_client_initializer: Optional[Callable] = None,
) -> Callable[[SlackBotToken, Optional[SlackAppToken]], SlackContext]:
    """Create a reusable Slack provider factory for a specific project.
    
    Args:
        web_client_initializer: Optional function to initialize web client
    
    Returns:
        Factory function that creates SlackContext from tokens
    """
    def factory(
        bot_token: SlackBotToken,
        app_token: Optional[SlackAppToken] = None,
        **kwargs,
    ) -> SlackContext:
        return create_slack_context(
            bot_token=bot_token,
            app_token=app_token,
            web_client_initializer=web_client_initializer,
            **kwargs,
        )
    return factory


def create_rabbitmq_provider_factory(
    rabbitmq_initializer: Optional[Callable] = None,
) -> Callable:
    """Create a reusable RabbitMQ provider factory for a specific project.
    
    Args:
        rabbitmq_initializer: Optional function to initialize RabbitMQ
    
    Returns:
        Factory function that creates RabbitMQContext from parameters
    """
    def factory(
        host: RabbitMQHost,
        port: RabbitMQPort = 5672,
        user: RabbitMQUser = None,
        password: RabbitMQPassword = None,
        vhost: RabbitMQVHost = "/",
        enable_ssl: RabbitMQEnableSSL = False,
        ssl_hostname: RabbitMQSSLHostname = None,
        **kwargs,
    ) -> RabbitMQContext:
        return create_rabbitmq_context(
            host=host,
            port=port,
            user=user,
            password=password,
            vhost=vhost,
            enable_ssl=enable_ssl,
            ssl_hostname=ssl_hostname,
            rabbitmq_initializer=rabbitmq_initializer,
            **kwargs,
        )
    return factory
