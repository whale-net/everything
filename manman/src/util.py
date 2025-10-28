import concurrent.futures
import io
import logging
import os
import threading
from typing import Optional

from sqlmodel import Session

from libs.python.postgres import create_engine as create_postgres_engine
from manman.src.repository.api_client import AuthAPIClient
from libs.python.rmq import (
    cleanup_rabbitmq_connections,
    create_rabbitmq_vhost,
    get_rabbitmq_connection,
    get_rabbitmq_ssl_options,
    init_rabbitmq,
)

logger = logging.getLogger(__name__)

__GLOBALS = {}


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


def get_sqlalchemy_engine():
    """Get the global SQLAlchemy engine."""
    if __GLOBALS.get("engine") is None:
        raise RuntimeError("global engine not defined - cannot start")
    return __GLOBALS["engine"]


def init_sql_alchemy_engine(
    connection_string: str,
    force_reinit: bool = False,
):
    """
    Initialize the global SQLAlchemy engine with production-ready pool settings.
    
    Uses libs.python.postgres.create_engine() which provides:
    - pool_size=20 (configurable via SQLALCHEMY_POOL_SIZE)
    - max_overflow=30 (configurable via SQLALCHEMY_MAX_OVERFLOW)
    - pool_recycle=3600 (configurable via SQLALCHEMY_POOL_RECYCLE)
    - pool_pre_ping=True
    
    This supports up to 50 concurrent database operations per process.
    
    Args:
        connection_string: Database connection string
        force_reinit: If True, allows re-initialization (useful for worker processes)
    """
    if "engine" in __GLOBALS and not force_reinit:
        logger.warning("Engine already initialized, skipping re-initialization")
        return
    __GLOBALS["engine"] = create_postgres_engine(connection_string)


def get_sqlalchemy_session(session: Optional[Session] = None) -> Session:
    # TODO : apply lessons from fcm on session management. this doesn't seem right.
    if session is not None:
        return session
    return Session(get_sqlalchemy_engine())


def env_list_to_dict(env_list: list[str], install_dir: str | None = None) -> dict[str, str]:
    """Convert a list of environment variables to a dictionary.
    
    Supports variable expansion for paths relative to the installation directory:
    - $INSTALL_DIR or ${INSTALL_DIR} will be replaced with the actual install directory
    
    Args:
        env_list: List of "KEY=VALUE" strings
        install_dir: Optional installation directory for variable expansion
        
    Returns:
        Dictionary of environment variables with resolved paths
        
    Example:
        env_list = ["LD_LIBRARY_PATH=$INSTALL_DIR/game/bin:$INSTALL_DIR/csgo/bin"]
        install_dir = "/app/data/steam/730/server"
        result = {"LD_LIBRARY_PATH": "/app/data/steam/730/server/game/bin:/app/data/steam/730/server/csgo/bin"}
    """
    env_dict = {}
    for env in env_list:
        if "=" not in env:
            raise ValueError(f"Invalid environment variable: {env}")
        key, value = env.split("=", 1)
        
        # Resolve $INSTALL_DIR variables if install_dir is provided
        if install_dir and ("$INSTALL_DIR" in value or "${INSTALL_DIR}" in value):
            value = value.replace("${INSTALL_DIR}", install_dir)
            value = value.replace("$INSTALL_DIR", install_dir)
        
        env_dict[key] = value
    return env_dict


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
