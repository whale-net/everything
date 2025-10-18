"""RabbitMQ provider with SSL support.

Provides RabbitMQ connection context with optional SSL configuration.

Example:
    ```python
    from libs.python.cli.providers.rabbitmq import RabbitMQHost, create_rabbitmq_context

    app = typer.Typer()

    @app.callback()
    def setup(
        ctx: typer.Context,
        rabbitmq_host: RabbitMQHost,
        rabbitmq_port: RabbitMQPort = 5672,
    ):
        ctx.obj = create_rabbitmq_context(
            host=rabbitmq_host,
            port=rabbitmq_port,
        )
    ```
"""

import logging
from dataclasses import dataclass
from typing import Annotated, Optional

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
