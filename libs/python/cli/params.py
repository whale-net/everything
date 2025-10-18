"""
Parameter groups for CLI commands using stackable decorators.

This module provides decorators that inject parameter groups into Typer callbacks,
dramatically reducing function signatures while keeping all CLI options visible.

Stack decorators to add multiple service parameter groups:
    ```python
    from libs.python.cli.params import rmq_params, pg_params, slack_params
    
    @app.callback()
    @rmq_params      # Adds 7 RabbitMQ parameters
    @pg_params       # Adds 1 Postgres parameter
    @slack_params    # Adds 1-2 Slack parameters
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

Available decorators:
- @rmq_params: RabbitMQ connection (7 params)
- @pg_params: PostgreSQL database (1 param)
- @slack_params: Slack authentication (1-2 params)
- @logging_params: Logging configuration (1 param)
"""

from dataclasses import dataclass
from functools import wraps
from typing import Annotated, Callable, get_type_hints
import inspect

import typer

# ==============================================================================
# Type Aliases for Common Parameters
# ==============================================================================

# RabbitMQ
RabbitMQHost = Annotated[str, typer.Option("--rabbitmq-host", envvar="RABBITMQ_HOST")]
RabbitMQPort = Annotated[int, typer.Option("--rabbitmq-port", envvar="RABBITMQ_PORT")]
RabbitMQUser = Annotated[str, typer.Option("--rabbitmq-user", envvar="RABBITMQ_USER")]
RabbitMQPassword = Annotated[str, typer.Option("--rabbitmq-password", envvar="RABBITMQ_PASSWORD")]
RabbitMQVhost = Annotated[str, typer.Option("--rabbitmq-vhost", envvar="RABBITMQ_VHOST")]
RabbitMQEnableSSL = Annotated[bool, typer.Option("--rabbitmq-enable-ssl", envvar="RABBITMQ_ENABLE_SSL")]
RabbitMQSSLHostname = Annotated[str, typer.Option("--rabbitmq-ssl-hostname", envvar="RABBITMQ_SSL_HOSTNAME")]

# Slack
SlackBotToken = Annotated[str, typer.Option("--slack-bot-token", envvar="SLACK_BOT_TOKEN")]
SlackAppToken = Annotated[str, typer.Option("--slack-app-token", envvar="SLACK_APP_TOKEN")]

# PostgreSQL
PostgresURL = Annotated[str, typer.Option("--database-url", envvar="DATABASE_URL")]

# Logging
EnableOTLP = Annotated[bool, typer.Option("--log-otlp", help="Enable OTLP logging")]


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
# Base Decorator Factory
# ==============================================================================

def _create_param_decorator(
    param_specs: list[tuple[str, inspect.Parameter]],
    context_key: str,
    param_extractor: Callable,
) -> Callable:
    """
    Factory for creating parameter injection decorators.
    
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
# Service-Specific Decorators
# ==============================================================================

def rmq_params(func: Callable) -> Callable:
    """
    Decorator that injects RabbitMQ parameters into the callback.
    
    Reduces callback signature from N+7 to N parameters while keeping
    all 7 RabbitMQ options visible in CLI help.
    
    Usage:
        @app.callback()
        @rmq_params
        def callback(ctx: typer.Context, ...):
            rmq = ctx.obj['rabbitmq']
            # rmq = {'host': ..., 'port': ..., 'user': ..., ...}
    """
    param_specs = [
        ('rabbitmq_host', inspect.Parameter(
            'rabbitmq_host', inspect.Parameter.KEYWORD_ONLY,
            default="localhost", annotation=RabbitMQHost
        )),
        ('rabbitmq_port', inspect.Parameter(
            'rabbitmq_port', inspect.Parameter.KEYWORD_ONLY,
            default=5672, annotation=RabbitMQPort
        )),
        ('rabbitmq_user', inspect.Parameter(
            'rabbitmq_user', inspect.Parameter.KEYWORD_ONLY,
            default="guest", annotation=RabbitMQUser
        )),
        ('rabbitmq_password', inspect.Parameter(
            'rabbitmq_password', inspect.Parameter.KEYWORD_ONLY,
            default="guest", annotation=RabbitMQPassword
        )),
        ('rabbitmq_vhost', inspect.Parameter(
            'rabbitmq_vhost', inspect.Parameter.KEYWORD_ONLY,
            default="/", annotation=RabbitMQVhost
        )),
        ('rabbitmq_enable_ssl', inspect.Parameter(
            'rabbitmq_enable_ssl', inspect.Parameter.KEYWORD_ONLY,
            default=False, annotation=RabbitMQEnableSSL
        )),
        ('rabbitmq_ssl_hostname', inspect.Parameter(
            'rabbitmq_ssl_hostname', inspect.Parameter.KEYWORD_ONLY,
            default="", annotation=RabbitMQSSLHostname
        )),
    ]
    
    def extractor(kwargs):
        return {
            'host': kwargs.pop('rabbitmq_host', 'localhost'),
            'port': kwargs.pop('rabbitmq_port', 5672),
            'user': kwargs.pop('rabbitmq_user', 'guest'),
            'password': kwargs.pop('rabbitmq_password', 'guest'),
            'vhost': kwargs.pop('rabbitmq_vhost', '/'),
            'enable_ssl': kwargs.pop('rabbitmq_enable_ssl', False),
            'ssl_hostname': kwargs.pop('rabbitmq_ssl_hostname', ''),
        }
    
    return _create_param_decorator(param_specs, 'rabbitmq', extractor)(func)


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
    param_specs = [
        ('database_url', inspect.Parameter(
            'database_url', inspect.Parameter.KEYWORD_ONLY,
            annotation=PostgresURL
        )),
    ]
    
    def extractor(kwargs):
        return {
            'database_url': kwargs.pop('database_url'),
        }
    
    return _create_param_decorator(param_specs, 'postgres', extractor)(func)


def slack_params(func: Callable) -> Callable:
    """
    Decorator that injects Slack parameters into the callback.
    
    Usage:
        @app.callback()
        @slack_params
        def callback(ctx: typer.Context, ...):
            slack = ctx.obj['slack']
            # slack = {'bot_token': '...', 'app_token': '...'}
    """
    param_specs = [
        ('slack_bot_token', inspect.Parameter(
            'slack_bot_token', inspect.Parameter.KEYWORD_ONLY,
            annotation=SlackBotToken
        )),
        ('slack_app_token', inspect.Parameter(
            'slack_app_token', inspect.Parameter.KEYWORD_ONLY,
            default="", annotation=SlackAppToken
        )),
    ]
    
    def extractor(kwargs):
        return {
            'bot_token': kwargs.pop('slack_bot_token'),
            'app_token': kwargs.pop('slack_app_token', ''),
        }
    
    return _create_param_decorator(param_specs, 'slack', extractor)(func)


def logging_params(func: Callable) -> Callable:
    """
    Decorator that injects logging parameters into the callback.
    
    Usage:
        @app.callback()
        @logging_params
        def callback(ctx: typer.Context, ...):
            log_config = ctx.obj['logging']
            # log_config = {'enable_otlp': True/False}
    """
    param_specs = [
        ('log_otlp', inspect.Parameter(
            'log_otlp', inspect.Parameter.KEYWORD_ONLY,
            default=False, annotation=EnableOTLP
        )),
    ]
    
    def extractor(kwargs):
        return {
            'enable_otlp': kwargs.pop('log_otlp', False),
        }
    
    return _create_param_decorator(param_specs, 'logging', extractor)(func)


# Backwards compatibility
common_params = lambda **groups: rmq_params if groups.get('rabbitmq') else lambda f: f
