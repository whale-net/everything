import logging
import os

import amqpstorm
import typer
from typing_extensions import Annotated, Optional

from manman.src.config import ManManConfig
from manman.src.logging_config import setup_logging
from manman.src.util import get_rabbitmq_connection, get_rabbitmq_ssl_options, init_rabbitmq
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
def callback(
    # auth_url: Annotated[str, typer.Option(envvar="MANMAN_AUTH_URL")],
    rabbitmq_host: Annotated[str, typer.Option(envvar="MANMAN_RABBITMQ_HOST")],
    rabbitmq_port: Annotated[int, typer.Option(envvar="MANMAN_RABBITMQ_PORT")],
    rabbitmq_username: Annotated[str, typer.Option(envvar="MANMAN_RABBITMQ_USER")],
    rabbitmq_password: Annotated[str, typer.Option(envvar="MANMAN_RABBITMQ_PASSWORD")],
    app_env: Annotated[Optional[str], typer.Option(envvar="APP_ENV")] = None,
    enable_ssl: Annotated[
        bool, typer.Option(envvar="MANMAN_RABBITMQ_ENABLE_SSL")
    ] = False,
    rabbitmq_ssl_hostname: Annotated[
        str, typer.Option(envvar="MANMAN_RABBITMQ_SSL_HOSTNAME")
    ] = None,
):
    # Setup logging first
    setup_logging(microservice_name=ManManConfig.WORKER, app_env=app_env)

    virtual_host = f"manman-{app_env}" if app_env else "/"

    # Initialize with AMQPStorm connection parameters
    init_rabbitmq(
        host=rabbitmq_host,
        port=rabbitmq_port,
        username=rabbitmq_username,
        password=rabbitmq_password,
        virtual_host=virtual_host,
        ssl_enabled=enable_ssl,
        ssl_options=get_rabbitmq_ssl_options(rabbitmq_ssl_hostname)
        if enable_ssl
        else None,
    )


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


def main():
    """Main entry point for the CLI."""
    app()


if __name__ == "__main__":
    main()
