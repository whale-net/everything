"""
RabbitMQ connection management.

This module provides connection pooling and management for RabbitMQ connections,
including SSL support and per-process connection handling.
"""

import logging
import os
import ssl
import threading
from typing import Optional

import amqpstorm

logger = logging.getLogger(__name__)

# Global state for connection management
__GLOBALS = {}
_connection_lock = threading.Lock()


def init_rabbitmq(
    host: str,
    port: int,
    username: str,
    password: str,
    virtual_host: str = "/",
    ssl_enabled: bool = False,
    ssl_options: Optional[dict] = None,
) -> None:
    """
    Initialize RabbitMQ connection parameters for later use.
    
    Args:
        host: RabbitMQ server hostname
        port: RabbitMQ server port (usually 5672 or 5671 for SSL)
        username: Authentication username
        password: Authentication password
        virtual_host: Virtual host to connect to (default: "/")
        ssl_enabled: Whether to use SSL/TLS
        ssl_options: SSL configuration dict (required if ssl_enabled=True)
    """
    __GLOBALS["rmq_parameters"] = {
        "host": host,
        "port": port,
        "username": username,
        "password": password,
        "virtual_host": virtual_host,
        "ssl": ssl_enabled,
        "ssl_options": ssl_options,
    }
    logger.info("RabbitMQ parameters stored")


def get_rabbitmq_ssl_options(hostname: str) -> dict:
    """
    Create SSL options for RabbitMQ connection.
    
    Args:
        hostname: Server hostname for SSL certificate verification
        
    Returns:
        Dictionary with SSL context and server hostname
        
    Raises:
        RuntimeError: If hostname is empty or None
    """
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.load_default_certs(purpose=ssl.Purpose.SERVER_AUTH)
    if hostname is None or len(hostname) == 0:
        raise RuntimeError(
            "SSL is enabled but no hostname provided. "
            "Please set RABBITMQ_SSL_HOSTNAME"
        )
    ssl_options = {
        "context": context,
        "server_hostname": hostname,
    }
    return ssl_options


def get_rabbitmq_connection() -> amqpstorm.Connection:
    """
    Get or create a RabbitMQ connection for the current process.

    Creates one persistent connection per process (including Gunicorn workers).
    Each process gets its own connection with a fresh SSL context to avoid
    SSL context sharing issues across forked processes.
    
    Returns:
        Active RabbitMQ connection
        
    Raises:
        RuntimeError: If init_rabbitmq() has not been called
    """
    with _connection_lock:
        # Check if we have a valid connection for this process
        current_pid = os.getpid()
        connection_key = f"rmq_connection_{current_pid}"

        if connection_key in __GLOBALS:
            connection = __GLOBALS[connection_key]
            try:
                # Test if connection is still alive
                if connection.is_open:
                    return connection
                else:
                    logger.warning("RabbitMQ connection is closed, creating new one")
            except Exception as e:
                logger.warning("Error checking RabbitMQ connection status: %s", e)

            # Remove invalid connection
            del __GLOBALS[connection_key]

        # Create new connection for this process
        if "rmq_parameters" not in __GLOBALS:
            raise RuntimeError(
                "rmq_parameters not defined - init_rabbitmq() must be called first"
            )

        params = __GLOBALS["rmq_parameters"]

        # Create fresh SSL options for this process to avoid context sharing issues
        ssl_options = None
        if params["ssl"] and params.get("ssl_options"):
            # Recreate SSL context to avoid fork issues
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
            context.load_default_certs(purpose=ssl.Purpose.SERVER_AUTH)
            ssl_options = {
                "context": context,
                "server_hostname": params["ssl_options"]["server_hostname"],
            }

        try:
            connection = amqpstorm.Connection(
                hostname=params["host"],
                port=params["port"],
                username=params["username"],
                password=params["password"],
                virtual_host=params["virtual_host"],
                ssl=params["ssl"],
                ssl_options=ssl_options,
            )
            __GLOBALS[connection_key] = connection
            logger.info("RabbitMQ connection established for process %d", current_pid)
            return connection
        except Exception as e:
            logger.error("Failed to create RabbitMQ connection: %s", e)
            raise


def cleanup_rabbitmq_connections() -> None:
    """
    Cleanup function to gracefully close RabbitMQ connections.

    Should be called during application shutdown or worker termination.
    """
    with _connection_lock:
        current_pid = os.getpid()
        connection_key = f"rmq_connection_{current_pid}"

        if connection_key in __GLOBALS:
            connection = __GLOBALS[connection_key]
            try:
                if connection.is_open:
                    connection.close()
                    logger.info(
                        "RabbitMQ connection closed for process %d", current_pid
                    )
            except Exception as e:
                logger.warning("Error closing RabbitMQ connection: %s", e)
            finally:
                del __GLOBALS[connection_key]


def create_rabbitmq_vhost(
    host: str,
    port: int,
    username: str,
    password: str,
    vhost: str,
) -> None:
    """
    Create a RabbitMQ virtual host using the management HTTP API.
    
    Args:
        host: RabbitMQ server hostname
        port: RabbitMQ AMQP port (note: uses port 15672 for management API)
        username: Admin username
        password: Admin password
        vhost: Virtual host name to create
        
    Raises:
        RuntimeError: If amqpstorm.management is not available
    """
    try:
        from amqpstorm.management import ManagementApi
    except ImportError:
        raise RuntimeError("amqpstorm.management is required for vhost creation")
    
    logger.info("Creating vhost using management API on port 15672 (ignoring AMQP port %s)", port)
    mgmt = ManagementApi(
        api_url=f"http://{host}:15672",
        username=username,
        password=password,
    )
    mgmt.virtual_host.create(vhost)
    logger.info("RabbitMQ vhost created: %s", vhost)
