import logging
import os

import amqpstorm
import typer
from typing_extensions import Annotated, Optional

from libs.python.cli.providers.rabbitmq import rmq_params
from libs.python.cli.types import AppEnv
from libs.python.logging import configure_logging
from manman.src.config import ManManConfig
from manman.src.util import get_sqlalchemy_session
from libs.python.rmq import (
    get_rabbitmq_connection,
    init_rabbitmq_from_config,
)
from manman.src.worker.worker_service import WorkerService

app = typer.Typer()
logger = logging.getLogger(__name__)


@app.command()
def start(
    # sa_client_id: Annotated[str, typer.Option(envvar="MANMAN_WORKER_SA_CLIENT_ID")],
    # sa_client_secret: Annotated[
    #     str, typer.Option(envvar="MANMAN_WORKER_SA_CLIENT_SECRET")
    # ],
    host_url: Annotated[str, typer.Option(envvar="MANMAN_HOST_URL")],
    install_directory: Annotated[
        str, typer.Option(envvar="MANMAN_WORKER_INSTALL_DIRECTORY")
    ] = "./data",
    heartbeat_length: Annotated[
        int, typer.Option(help="Heartbeat interval in seconds (default: 2)")
    ] = 2,
    verify_ssl: Annotated[
        bool, typer.Option(envvar="MANMAN_WORKER_VERIFY_SSL", help="Verify SSL certificates (default: True)")
    ] = True,
    # steamcmd_override: Annotated[
    #     Optional[str], typer.Option(envvar="MANMAN_STEAMCMD_OVERRIDE"), None
    # ] = None,
):
    install_directory = os.path.abspath(install_directory)
    # todo - re-add authcz
    service = WorkerService(
        rabbitmq_connection=get_rabbitmq_connection(),
        install_directory=install_directory,
        host_url=host_url,
        sa_client_id=None,
        sa_client_secret=None,
        heartbeat_length=heartbeat_length,
        verify_ssl=verify_ssl,
    )
    service.run()


@app.command()
def dev():
    from manman.src.constants import EntityRegistry
    from manman.src.worker.abstract_service import ManManService

    class DevService(ManManService):
        @property
        def service_entity_type(self):
            return EntityRegistry.WORKER

        @property
        def identifier(self):
            return "dev_service"

        def __init__(self, connection: amqpstorm.Connection):
            super().__init__(connection)

        def _initialize_service(self):
            logger.info("DevService setup called")

        def _do_work(self):
            logger.info("DevService started")

        def _stop_service(self):
            logger.info("DevService stopped")

        def _handle_commands(self, commands):
            for command in commands:
                logger.info(f"DevService received command: {command}")

        def _send_heartbeat(self):
            logger.info("heartbeat sent from DevService")

    DevService(get_rabbitmq_connection()).run()


@app.callback()
@rmq_params
def callback(
    ctx: typer.Context,
    app_env: AppEnv = None,
):
    # Setup logging - CONSOLE ONLY for manman-worker (human readable)
    configure_logging(
        app_name=f"{ManManConfig.WORKER}-{app_env}" if app_env else ManManConfig.WORKER,
        domain="manman",
        app_type="worker",
        environment=app_env or "development",
        log_level="DEBUG",
        enable_otlp=False,  # Worker runs externally - no OTLP
        enable_console=True,  # Console only
        json_format=False,  # Human readable text
    )
    
    # Get RabbitMQ config from decorator-injected params and initialize
    rmq_config = ctx.obj.get('rabbitmq', {})
    init_rabbitmq_from_config(rmq_config, vhost_suffix=app_env)
    
    # Store context
    ctx.obj['app_env'] = app_env


@app.command()
def localdev_send_queue(key: int):
    connection = get_rabbitmq_connection()
    chan = connection.channel()
    chan.exchange.declare(exchange="server", exchange_type="direct")

    from manman.src.models import Command, CommandType

    shutdown_command = Command(command_type=CommandType.STOP)
    message = amqpstorm.Message.create(
        chan,
        body=shutdown_command.model_dump_json(),
        properties={"content_type": "application/json"},
    )
    message.publish(exchange="server", routing_key=str(key))
    return


if __name__ == "__main__":
    app()
