"""
Parameter registry and decorator factory for CLI commands.

This module provides:
1. Re-exports of _create_param_decorator from params_base (for backwards compatibility)
2. Type aliases for common parameters (re-exported from providers)
3. Re-exports of decorators from provider modules for convenience

NOTE: For minimal dependencies, import directly from provider submodules:
    from libs.python.cli.providers.postgres import pg_params, PostgresUrl
    from libs.python.cli.providers.rabbitmq import rmq_params
    from libs.python.cli.types import AppEnv

Stack decorators to add multiple service parameter groups:
    ```python
    from libs.python.cli.params import rmq_params, pg_params, slack_params
    
    @app.callback()
    @rmq_params      # Adds 7 RabbitMQ parameters
    @pg_params       # Adds 1 Postgres parameter
    @slack_params    # Adds 2 Slack parameters
    def callback(ctx: typer.Context):
        # Access via ctx.obj dictionary
        rmq = ctx.obj['rabbitmq']
        pg = ctx.obj['postgres']
        slack = ctx.obj['slack']
    ```

Each decorator:
1. Dynamically adds parameters to the function signature
2. Collects them from CLI input
3. Stores them in ctx.obj[key] as a dict
4. Keeps all parameters visible in --help output

Available decorators (defined in their respective provider modules):
- @rmq_params: RabbitMQ connection (7 params) - from libs.python.cli.providers.rabbitmq
- @pg_params: PostgreSQL database (1 param) - from libs.python.cli.providers.postgres
- @slack_params: Slack authentication (2 params) - from libs.python.cli.providers.slack
- @logging_params: Logging configuration (1 param) - from libs.python.cli.providers.logging
"""

# ==============================================================================
# Base Decorator Factory - Re-exported from params_base
# ==============================================================================

from libs.python.cli.params_base import _create_param_decorator


# ==============================================================================
# Type Aliases - Re-exported from Providers
# ==============================================================================

# RabbitMQ
from libs.python.cli.providers.rabbitmq import (
    RabbitMQHost,
    RabbitMQPort,
    RabbitMQUser,
    RabbitMQPassword,
    RabbitMQVHost as RabbitMQVhost,  # Alias for backwards compatibility
    RabbitMQEnableSSL,
    RabbitMQSSLHostname,
)

# Slack
from libs.python.cli.providers.slack import (
    SlackBotToken,
    SlackAppToken,
)

# PostgreSQL
from libs.python.cli.providers.postgres import (
    PostgresUrl as PostgresURL,  # Alias for backwards compatibility
)

# Logging
from libs.python.cli.providers.logging import (
    EnableOTLP,
)


# ==============================================================================
# Decorator Re-exports
# ==============================================================================

from libs.python.cli.providers.rabbitmq import rmq_params
from libs.python.cli.providers.postgres import pg_params
from libs.python.cli.providers.slack import slack_params
from libs.python.cli.providers.logging import logging_params
from libs.python.cli.providers.temporal import temporal_params
from libs.python.cli.providers.gemini import gemini_params
from libs.python.cli.types import (
    AppEnv,
    ManManHostUrl,
    ManManExperienceApiUrl,
    ManManStatusApiUrl,
    ManManWorkerDalApiUrl,
)


__all__ = [
    # Factory
    '_create_param_decorator',
    
    # Type aliases - Common
    'AppEnv',
    'ManManHostUrl',
    'ManManExperienceApiUrl',
    'ManManStatusApiUrl',
    'ManManWorkerDalApiUrl',
    
    # Type aliases - RabbitMQ
    'RabbitMQHost',
    'RabbitMQPort',
    'RabbitMQUser',
    'RabbitMQPassword',
    'RabbitMQVhost',
    'RabbitMQEnableSSL',
    'RabbitMQSSLHostname',
    
    # Type aliases - Slack
    'SlackBotToken',
    'SlackAppToken',
    
    # Type aliases - PostgreSQL
    'PostgresURL',
    
    # Type aliases - Logging
    'EnableOTLP',
    
    # Decorators
    'rmq_params',
    'pg_params',
    'slack_params',
    'logging_params',
    'temporal_params',
    'gemini_params',
]

