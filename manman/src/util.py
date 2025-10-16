import concurrent.futures
import io
import logging
import os
import threading
from typing import Optional

import sqlalchemy
from sqlmodel import Session

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


def env_list_to_dict(env_list: list[str]) -> dict[str, str]:
    """Convert a list of environment variables to a dictionary."""
    env_dict = {}
    for env in env_list:
        if "=" not in env:
            raise ValueError(f"Invalid environment variable: {env}")
        key, value = env.split("=", 1)
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
