"""RabbitMQ provider with SSL support.

Provides RabbitMQ connection context with optional SSL configuration.

Example:
    ```python
    from libs.python.cli.providers.rabbitmq import rmq_params

    app = typer.Typer()

    @app.callback()
    @rmq_params
    def setup(ctx: typer.Context):
        rmq = ctx.obj['rabbitmq']
        # rmq = {'host': ..., 'port': ..., 'user': ..., ...}
    ```
"""

import inspect
import logging
from dataclasses import dataclass
from functools import wraps
from typing import Annotated, Callable, Optional

import typer

logger = logging.getLogger(__name__)


# Type aliases for CLI parameters
RabbitMQHost = Annotated[str, typer.Option(..., envvar="RABBITMQ_HOST")]
RabbitMQPort = Annotated[int, typer.Option(envvar="RABBITMQ_PORT")]
RabbitMQUser = Annotated[Optional[str], typer.Option(envvar="RABBITMQ_USER")]
RabbitMQPassword = Annotated[Optional[str], typer.Option(envvar="RABBITMQ_PASSWORD")]
RabbitMQVHost = Annotated[Optional[str], typer.Option(envvar="RABBITMQ_VHOST")]
RabbitMQEnableSSL = Annotated[bool, typer.Option(envvar="RABBITMQ_ENABLE_SSL")]
RabbitMQSSLHostname = Annotated[
    Optional[str], typer.Option(envvar="RABBITMQ_SSL_HOSTNAME")
]


@dataclass
class RabbitMQContext:
    """Typed RabbitMQ context with connection configuration.
    
    Attributes:
        host: RabbitMQ host
        port: RabbitMQ port
        user: Optional username
        password: Optional password
        vhost: Virtual host (defaults to "/")
        enable_ssl: Whether SSL is enabled
        ssl_hostname: Optional SSL hostname for verification
    """

    host: str
    port: int
    user: Optional[str] = None
    password: Optional[str] = None
    vhost: str = "/"
    enable_ssl: bool = False
    ssl_hostname: Optional[str] = None


def create_rabbitmq_context(
    host: RabbitMQHost,
    port: RabbitMQPort = 5672,
    user: RabbitMQUser = None,
    password: RabbitMQPassword = None,
    vhost: RabbitMQVHost = "/",
    enable_ssl: RabbitMQEnableSSL = False,
    ssl_hostname: RabbitMQSSLHostname = None,
    rabbitmq_initializer: Optional[callable] = None,
) -> RabbitMQContext:
    """Create RabbitMQ context with connection configuration.
    
    Args:
        host: RabbitMQ host
        port: RabbitMQ port (default: 5672)
        user: Optional username
        password: Optional password
        vhost: Virtual host (default: "/")
        enable_ssl: Enable SSL connections
        ssl_hostname: SSL hostname for verification
        rabbitmq_initializer: Optional function to initialize RabbitMQ connection
            Should accept RabbitMQContext and perform initialization
    
    Returns:
        RabbitMQContext with configured connection parameters
        
    Example:
        >>> def init_rmq(ctx):
        ...     from libs.python.rmq import init_rabbitmq
        ...     init_rabbitmq(
        ...         rabbitmq_host=ctx.host,
        ...         rabbitmq_port=ctx.port,
        ...         rabbitmq_user=ctx.user,
        ...         rabbitmq_password=ctx.password,
        ...     )
        >>> ctx = create_rabbitmq_context(
        ...     host="localhost",
        ...     port=5672,
        ...     rabbitmq_initializer=init_rmq,
        ... )
    """
    logger.debug("Creating RabbitMQ context for host: %s:%s", host, port)

    ctx = RabbitMQContext(
        host=host,
        port=port,
        user=user,
        password=password,
        vhost=vhost,
        enable_ssl=enable_ssl,
        ssl_hostname=ssl_hostname,
    )

    # Run custom initialization if provided
    if rabbitmq_initializer:
        logger.debug("Running RabbitMQ initializer")
        rabbitmq_initializer(ctx)

    logger.debug("RabbitMQ context created successfully")

    return ctx


# ==============================================================================
# Decorator for injecting RabbitMQ parameters
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
    from libs.python.cli.params import _create_param_decorator
    
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
            default="/", annotation=RabbitMQVHost
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
