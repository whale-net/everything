"""
Parameter groups for CLI commands.

This module provides reusable parameter type aliases and builder functions
that reduce boilerplate when working with common parameter groups.

The pattern:
1. Define type aliases for parameters (with Annotated types)
2. Use builder functions to construct contexts from parameters
3. Keep all parameters visible in function signatures for IDE support

Example:
    ```python
    from libs.python.cli.params import (
        RabbitMQHost, RabbitMQPort, ...,  # Type aliases
        build_rabbitmq_context,  # Builder function
    )
    
    @app.callback()
    def callback(
        ctx: typer.Context,
        slack_token: SlackBotToken,
        rabbitmq_host: RabbitMQHost = "localhost",
        rabbitmq_port: RabbitMQPort = 5672,
        # ... 5 more rabbitmq params
    ):
        # Build context in one line
        rmq = build_rabbitmq_context(
            rabbitmq_host, rabbitmq_port, rabbitmq_user,
            rabbitmq_password, rabbitmq_vhost, rabbitmq_enable_ssl,
            rabbitmq_ssl_hostname
        )
    ```

Alternative: Use __common_params decorator to hide params from your function
while keeping them in CLI help (advanced pattern).
"""

from dataclasses import dataclass
from functools import wraps
from typing import Annotated, Callable, get_type_hints
import inspect

import typer

# ==============================================================================
# Type Aliases for Common Parameters
# ==============================================================================

RabbitMQHost = Annotated[str, typer.Option("--rabbitmq-host", envvar="RABBITMQ_HOST")]
RabbitMQPort = Annotated[int, typer.Option("--rabbitmq-port", envvar="RABBITMQ_PORT")]
RabbitMQUser = Annotated[str, typer.Option("--rabbitmq-user", envvar="RABBITMQ_USER")]
RabbitMQPassword = Annotated[str, typer.Option("--rabbitmq-password", envvar="RABBITMQ_PASSWORD")]
RabbitMQVhost = Annotated[str, typer.Option("--rabbitmq-vhost", envvar="RABBITMQ_VHOST")]
RabbitMQEnableSSL = Annotated[bool, typer.Option("--rabbitmq-enable-ssl", envvar="RABBITMQ_ENABLE_SSL")]
RabbitMQSSLHostname = Annotated[str, typer.Option("--rabbitmq-ssl-hostname", envvar="RABBITMQ_SSL_HOSTNAME")]

SlackBotToken = Annotated[str, typer.Option("--slack-bot-token", envvar="SLACK_BOT_TOKEN")]
SlackAppToken = Annotated[str, typer.Option("--slack-app-token", envvar="SLACK_APP_TOKEN")]


# ==============================================================================
# Builder Functions - Simplest Pattern
# ==============================================================================

def build_rabbitmq_context(
    host: str,
    port: int,
    user: str,
    password: str,
    vhost: str,
    enable_ssl: bool,
    ssl_hostname: str,
):
    """
    Build RabbitMQ context from individual parameters.
    
    Reduces boilerplate from:
        create_rabbitmq_context(
            host=rabbitmq_host,
            port=rabbitmq_port,
            user=rabbitmq_user,
            password=rabbitmq_password,
            vhost=rabbitmq_vhost,
            enable_ssl=rabbitmq_enable_ssl,
            ssl_hostname=rabbitmq_ssl_hostname,
        )
    
    To:
        build_rabbitmq_context(
            rabbitmq_host, rabbitmq_port, rabbitmq_user,
            rabbitmq_password, rabbitmq_vhost, rabbitmq_enable_ssl,
            rabbitmq_ssl_hostname
        )
    
    Usage in CLI callback:
        def callback(
            ctx: typer.Context,
            rabbitmq_host: RabbitMQHost = "localhost",
            rabbitmq_port: RabbitMQPort = 5672,
            rabbitmq_user: RabbitMQUser = "guest",
            rabbitmq_password: RabbitMQPassword = "guest",
            rabbitmq_vhost: RabbitMQVhost = "/",
            rabbitmq_enable_ssl: RabbitMQEnableSSL = False,
            rabbitmq_ssl_hostname: RabbitMQSSLHostname = "",
        ):
            rmq = build_rabbitmq_context(
                rabbitmq_host, rabbitmq_port, rabbitmq_user,
                rabbitmq_password, rabbitmq_vhost, rabbitmq_enable_ssl,
                rabbitmq_ssl_hostname
            )
    """
    from libs.python.cli.providers.rabbitmq import create_rabbitmq_context
    
    return create_rabbitmq_context(
        host=host,
        port=port,
        user=user,
        password=password,
        vhost=vhost,
        enable_ssl=enable_ssl,
        ssl_hostname=ssl_hostname,
    )


# ==============================================================================
# Advanced: Common Params Decorator (experimental)
# ==============================================================================

def common_params(**param_groups):
    """
    EXPERIMENTAL: Decorator that adds common parameter groups to a function.
    
    This allows you to write:
        @app.callback()
        @common_params(rabbitmq=True)
        def callback(ctx: typer.Context, slack_token: str):
            rmq = ctx.obj['rabbitmq']
    
    The decorator dynamically adds the parameter group to the function signature
    so Typer can discover them for CLI help.
    
    Note: This is experimental and may not work with all Typer features.
    """
    def decorator(func: Callable) -> Callable:
        # Get the original signature
        sig = inspect.signature(func)
        params = list(sig.parameters.values())
        
        # If rabbitmq group requested, add those params
        if param_groups.get('rabbitmq'):
            # Add RabbitMQ parameters to the signature
            params.extend([
                inspect.Parameter(
                    'rabbitmq_host',
                    inspect.Parameter.KEYWORD_ONLY,
                    default="localhost",
                    annotation=RabbitMQHost
                ),
                inspect.Parameter(
                    'rabbitmq_port',
                    inspect.Parameter.KEYWORD_ONLY,
                    default=5672,
                    annotation=RabbitMQPort
                ),
                inspect.Parameter(
                    'rabbitmq_user',
                    inspect.Parameter.KEYWORD_ONLY,
                    default="guest",
                    annotation=RabbitMQUser
                ),
                inspect.Parameter(
                    'rabbitmq_password',
                    inspect.Parameter.KEYWORD_ONLY,
                    default="guest",
                    annotation=RabbitMQPassword
                ),
                inspect.Parameter(
                    'rabbitmq_vhost',
                    inspect.Parameter.KEYWORD_ONLY,
                    default="/",
                    annotation=RabbitMQVhost
                ),
                inspect.Parameter(
                    'rabbitmq_enable_ssl',
                    inspect.Parameter.KEYWORD_ONLY,
                    default=False,
                    annotation=RabbitMQEnableSSL
                ),
                inspect.Parameter(
                    'rabbitmq_ssl_hostname',
                    inspect.Parameter.KEYWORD_ONLY,
                    default="",
                    annotation=RabbitMQSSLHostname
                ),
            ])
        
        # Create new signature
        new_sig = sig.replace(parameters=params)
        
        @wraps(func)
        def wrapper(*args, **kwargs):
            # Extract RabbitMQ params from kwargs
            if param_groups.get('rabbitmq'):
                ctx = args[0] if args else kwargs.get('ctx')
                if ctx:
                    ctx.ensure_object(dict)
                    ctx.obj['rabbitmq'] = {
                        'host': kwargs.pop('rabbitmq_host', 'localhost'),
                        'port': kwargs.pop('rabbitmq_port', 5672),
                        'user': kwargs.pop('rabbitmq_user', 'guest'),
                        'password': kwargs.pop('rabbitmq_password', 'guest'),
                        'vhost': kwargs.pop('rabbitmq_vhost', '/'),
                        'enable_ssl': kwargs.pop('rabbitmq_enable_ssl', False),
                        'ssl_hostname': kwargs.pop('rabbitmq_ssl_hostname', ''),
                    }
            
            # Call original function
            return func(*args, **kwargs)
        
        # Update wrapper's signature so Typer can inspect it
        wrapper.__signature__ = new_sig
        return wrapper
    
    return decorator
