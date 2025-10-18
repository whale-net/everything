"""
Parameter registry and decorator factory for CLI commands.

This module provides:
1. A base decorator factory (_create_param_decorator) used by all provider modules
2. Type aliases for common parameters (re-exported from providers)
3. Re-exports of decorators from provider modules for convenience

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

from functools import wraps
from typing import Callable
import inspect


# ==============================================================================
# Base Decorator Factory
# ==============================================================================

def _create_param_decorator(
    param_specs: list[tuple[str, inspect.Parameter]],
    context_key: str,
    param_extractor: Callable,
) -> Callable:
    """
    Factory for creating parameter injection decorators.
    
    Used by provider modules to create their decorators.
    
    Args:
        param_specs: List of (param_name, Parameter) tuples to inject
        context_key: Key to store extracted params in ctx.obj
        param_extractor: Function to extract params from kwargs into dict
    
    Returns:
        Decorator function that injects parameters
    """
    def decorator(func: Callable) -> Callable:
        # Get the original signature
        sig = inspect.signature(func)
        params = list(sig.parameters.values())
        
        # Add new parameters to signature
        params.extend([param for _, param in param_specs])
        
        # Create new signature
        new_sig = sig.replace(parameters=params)
        
        @wraps(func)
        def wrapper(*args, **kwargs):
            # Extract ctx from args (first positional arg in Typer callbacks)
            ctx = args[0] if args else kwargs.get('ctx')
            
            if ctx:
                ctx.ensure_object(dict)
                # Extract params using provided function
                ctx.obj[context_key] = param_extractor(kwargs)
            
            # Call original function
            return func(*args, **kwargs)
        
        # Update wrapper's signature so Typer can inspect it
        wrapper.__signature__ = new_sig
        return wrapper
    
    return decorator


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
from libs.python.cli.types import AppEnv


__all__ = [
    # Factory
    '_create_param_decorator',
    
    # Type aliases
    'RabbitMQHost',
    'RabbitMQPort',
    'RabbitMQUser',
    'RabbitMQPassword',
    'RabbitMQVhost',
    'RabbitMQEnableSSL',
    'RabbitMQSSLHostname',
    'SlackBotToken',
    'SlackAppToken',
    'PostgresURL',
    'EnableOTLP',
    'AppEnv',
    
    # Decorators
    'rmq_params',
    'pg_params',
    'slack_params',
    'logging_params',
]

