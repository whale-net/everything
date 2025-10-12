import logging
import os
from typing import Annotated, Optional

import typer

from friendly_computing_machine.rabbitmq.util import init_rabbitmq

T_rabbitmq_host = Annotated[str, typer.Option(..., envvar="FCM_RABBITMQ_HOST")]
T_rabbitmq_port = Annotated[
    Optional[int], typer.Option(..., envvar="FCM_RABBITMQ_PORT")
]
T_rabbitmq_user = Annotated[
    Optional[str], typer.Option(..., envvar="FCM_RABBITMQ_USER")
]
T_rabbitmq_password = Annotated[
    Optional[str], typer.Option(..., envvar="FCM_RABBITMQ_PASSWORD")
]
T_rabbitmq_enable_ssl = Annotated[
    Optional[bool], typer.Option(..., envvar="FCM_RABBITMQ_ENABLE_SSL")
]
T_rabbitmq_ssl_hostname = Annotated[
    Optional[str], typer.Option(..., envvar="FCM_RABBITMQ_SSL_HOSTNAME")
]
T_rabbitmq_vhost = Annotated[
    Optional[str], typer.Option(..., envvar="FCM_RABBITMQ_VHOST")
]

FILENAME = os.path.basename(__file__)
logger = logging.getLogger(__name__)


def setup_rabbitmq(
    ctx: typer.Context,
    rabbitmq_host: T_rabbitmq_host,
    rabbitmq_port: T_rabbitmq_port = None,
    rabbitmq_user: T_rabbitmq_user = None,
    rabbitmq_password: T_rabbitmq_password = None,
    rabbitmq_enable_ssl: T_rabbitmq_enable_ssl = None,
    rabbitmq_ssl_hostname: T_rabbitmq_ssl_hostname = None,
    rabbitmq_vhost: T_rabbitmq_vhost = None,
):
    logger.debug("rabbitmq setup starting")
    ctx.obj[FILENAME] = {
        "rabbitmq_host": rabbitmq_host,
        "rabbitmq_port": rabbitmq_port,
        "rabbitmq_user": rabbitmq_user,
        "rabbitmq_password": rabbitmq_password,
        "rabbitmq_enable_ssl": rabbitmq_enable_ssl,
        "rabbitmq_ssl_hostname": rabbitmq_ssl_hostname,
        "rabbitmq_vhost": rabbitmq_vhost,
    }
    init_rabbitmq(
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        rabbitmq_user=rabbitmq_user,
        rabbitmq_password=rabbitmq_password,
        rabbitmq_enable_ssl=rabbitmq_enable_ssl,
        rabbitmq_ssl_hostname=rabbitmq_ssl_hostname,
        rabbitmq_vhost=rabbitmq_vhost,
    )
    logger.debug("rabbitmq setup complete")
