"""Generic provider factory functions for creating project-specific wrappers.

This module provides factory functions that can be used to create reusable
provider wrappers for specific projects without hardcoding project-specific
initialization logic in the shared library.

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
