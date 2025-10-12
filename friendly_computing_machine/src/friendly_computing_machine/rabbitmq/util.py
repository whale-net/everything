import logging
from typing import Optional

import amqpstorm

logger = logging.getLogger(__name__)


class __global:
    rabbitmq_host = None
    rabbitmq_port = None
    rabbitmq_user = None
    rabbitmq_password = None
    rabbitmq_enable_ssl = None
    rabbitmq_ssl_hostname = None
    rabbitmq_vhost = None
    _connection = None


def init_rabbitmq(
    rabbitmq_host: Optional[str] = None,
    rabbitmq_port: Optional[int] = None,
    rabbitmq_user: Optional[str] = None,
    rabbitmq_password: Optional[str] = None,
    rabbitmq_enable_ssl: Optional[bool] = None,
    rabbitmq_ssl_hostname: Optional[str] = None,
    rabbitmq_vhost: Optional[str] = None,
):
    """
    Initialize the RabbitMQ connection settings in the global context.
    This is used to set up the RabbitMQ connection parameters for the application.
    """
    if __global._connection:
        raise RuntimeError(
            "RabbitMQ connection already initialized. Please use get_rabbitmq_connection() to access the connection."
        )

    __global.rabbitmq_host = rabbitmq_host
    __global.rabbitmq_port = rabbitmq_port
    __global.rabbitmq_user = rabbitmq_user
    __global.rabbitmq_password = rabbitmq_password
    __global.rabbitmq_enable_ssl = rabbitmq_enable_ssl
    __global.rabbitmq_ssl_hostname = rabbitmq_ssl_hostname
    __global.rabbitmq_vhost = rabbitmq_vhost

    logger.info("RabbitMQ configuration initialized")


def get_rabbitmq_connection() -> amqpstorm.Connection:
    """
    Get or create a global RabbitMQ connection.
    Returns the existing connection if it's open, otherwise creates a new one.
    """
    if __global._connection and not __global._connection.is_closed:
        return __global._connection

    # Use individual parameters
    host = __global.rabbitmq_host
    port = __global.rabbitmq_port
    username = __global.rabbitmq_user
    password = __global.rabbitmq_password
    ssl = __global.rabbitmq_enable_ssl

    try:
        logger.info(f"Connecting to RabbitMQ at {host}:{port} (SSL: {ssl})")

        ssl_options = {}
        if ssl:
            ssl_options["ssl"] = True
            if __global.rabbitmq_ssl_hostname:
                ssl_options["ssl_options"] = {
                    "server_hostname": __global.rabbitmq_ssl_hostname
                }

        __global._connection = amqpstorm.Connection(
            hostname=host,
            port=port,
            username=username,
            password=password,
            virtual_host=__global.rabbitmq_vhost or "/",
            **ssl_options,
        )

        logger.info("Successfully connected to RabbitMQ")
        return __global._connection

    except Exception as e:
        logger.error(f"Failed to connect to RabbitMQ: {e}")
        raise
