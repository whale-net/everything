import concurrent.futures
import io
import logging
import os
import ssl
import threading
from typing import Optional

import amqpstorm
import sqlalchemy
from sqlmodel import Session

from manman.repository.api_client import AuthAPIClient

logger = logging.getLogger(__name__)

__GLOBALS = {}
_connection_lock = threading.Lock()


def log_stream(
    stream: io.BufferedReader | None,
    prefix: str | None = None,
    logger: logging.Logger = logger,
    max_lines: int | None = None,
):
    if prefix is None:
        prefix = ""

    if stream is None:
        return

    line_count = 0
    while True:
        if max_lines is not None and line_count >= max_lines:
            break

        line = stream.readline()
        if line is None or len(line) == 0:
            break
        logger.info("%s%s", prefix, line.decode("utf-8").rstrip())
        line_count += 1 if max_lines is not None else 0


class NamedThreadPool(concurrent.futures.ThreadPoolExecutor):
    def submit(
        self, fn, /, name: Optional[str] = None, *args, **kwargs
    ) -> concurrent.futures.Future:  # type: ignore
        def rename_thread(*args, **kwargs):
            if name is not None and len(name) > 0:
                threading.current_thread().name = name
            fn(*args, **kwargs)

        return super().submit(rename_thread, *args, **kwargs)


def get_sqlalchemy_engine() -> sqlalchemy.engine:
    if __GLOBALS.get("engine") is None:
        raise RuntimeError("global engine not defined - cannot start")
    return __GLOBALS["engine"]


def init_sql_alchemy_engine(
    connection_string: str,
):
    if "engine" in __GLOBALS:
        return
    __GLOBALS["engine"] = sqlalchemy.create_engine(
        connection_string,
        pool_pre_ping=True,
    )


def get_sqlalchemy_session(session: Optional[Session] = None) -> Session:
    # TODO : apply lessons from fcm on session management. this doesn't seem right.
    if session is not None:
        return session
    return Session(get_sqlalchemy_engine())


# Update RabbitMQ functions to use AMQPStorm
def init_rabbitmq(
    host: str,
    port: int,
    username: str,
    password: str,
    virtual_host: str = "/",
    ssl_enabled: bool = False,
    ssl_options=None,
):
    """Initialize RabbitMQ connection parameters for later use."""
    __GLOBALS["rmq_parameters"] = {
        "host": host,
        "port": port,
        "username": username,
        "password": password,
        "virtual_host": virtual_host,
        "ssl": ssl_enabled,
        "ssl_options": ssl_options,
    }
    logger.info("rmq parameters stored")


def get_rabbitmq_ssl_options(hostname: str) -> dict:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.load_default_certs(purpose=ssl.Purpose.SERVER_AUTH)
    if hostname is None or len(hostname) == 0:
        raise RuntimeError(
            "SSL is enabled but no hostname provided. "
            "Please set MANMAN_RABBITMQ_SSL_HOSTNAME"
        )
    ssl_options = {
        #'context': ssl.create_default_context(cafile='ca_certificate.pem'),
        "context": context,
        "server_hostname": hostname,
        #'check_hostname': True,        # New 2.8.0, default is False
        #'verify_mode': 'required',     # New 2.8.0, default is 'none'
    }
    return ssl_options


def get_rabbitmq_connection() -> amqpstorm.Connection:
    """
    Get or create a RabbitMQ connection for the current process.

    Creates one persistent connection per process (including Gunicorn workers).
    Each process gets its own connection with a fresh SSL context to avoid
    SSL context sharing issues across forked processes.
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
            logger.info("rmq connection established for process %d", current_pid)
            return connection
        except Exception as e:
            logger.error("Failed to create RabbitMQ connection: %s", e)
            raise


def init_auth_api_client(auth_url: str):
    __GLOBALS["auth_api_client"] = AuthAPIClient(base_url=auth_url)


def get_auth_api_client() -> AuthAPIClient:
    # api_client = __GLOBALS.get("auth_api_client")
    # if api_client is None:
    #     raise RuntimeError("api_client is not initialized")
    # return api_client
    # TODO - re-add authcz
    from unittest.mock import MagicMock

    return MagicMock()


def env_list_to_dict(env_list: list[str]) -> dict[str, str]:
    """Convert a list of environment variables to a dictionary."""
    env_dict = {}
    for env in env_list:
        if "=" not in env:
            raise ValueError(f"Invalid environment variable: {env}")
        key, value = env.split("=", 1)
        env_dict[key] = value
    return env_dict


def create_rabbitmq_vhost(
    host: str,
    port: int,
    username: str,
    password: str,
    vhost: str,
):
    """Create a RabbitMQ virtual host using the management HTTP API via AMQPStorm."""
    try:
        from amqpstorm.management import ManagementApi
    except ImportError:
        raise RuntimeError("amqpstorm.management is required for vhost creation")
    logger.info("ignoring port %s using default 15672", port)
    mgmt = ManagementApi(
        # hardcoding port, whatever
        api_url=f"http://{host}:15672",
        username=username,
        password=password,
    )
    mgmt.virtual_host.create(vhost)
    logger.info("RabbitMQ vhost created: %s", vhost)


def cleanup_rabbitmq_connections():
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
