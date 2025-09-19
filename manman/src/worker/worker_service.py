import logging
import time
from datetime import timedelta
from threading import Lock

from amqpstorm import Connection
from requests import ConnectionError

from manman.src.models import (
    Command,
    CommandType,
    GameServerConfig,
)

# from sqlalchemy.orm import Session
from manman.src.repository.api_client import WorkerAPIClient
from manman.src.repository.rabbitmq.config import EntityRegistry
from manman.src.util import NamedThreadPool, get_auth_api_client
from manman.src.worker.abstract_service import ManManService
from manman.src.worker.server import Server

logger = logging.getLogger(__name__)


class WorkerService(ManManService):
    @property
    def service_entity_type(self) -> EntityRegistry:
        return EntityRegistry.WORKER

    @property
    def identifier(self) -> str:
        if not hasattr(self, "_worker_instance"):
            raise RuntimeError("Worker instance not initialized")
        if not hasattr(self._worker_instance, "worker_id"):
            raise RuntimeError("Worker instance does not have a worker_id")
        if self._worker_instance.worker_id is None:
            raise RuntimeError("Worker instance worker_id is None")
        # Return the worker ID as a string
        return str(self._worker_instance.worker_id)

    def __init__(
        self,
        rabbitmq_connection: Connection,
        *,
        install_directory: str,
        host_url: str,
        sa_client_id: str,
        sa_client_secret: str,
        heartbeat_length: int = 2,
    ):
        # pre-init
        self._wapi = WorkerAPIClient(
            host_url,
            auth_api_client=get_auth_api_client(),
            sa_client_id=sa_client_id,
            sa_client_secret=sa_client_secret,
        )

        try:
            self._worker_instance = self._wapi.worker_create()
        except ConnectionError as e:
            logger.exception(e)
            # if you see this while debugging, wait for tilt to start the api server
            raise RuntimeError("failed to connect to host - is it running?") from e
        except Exception as e:
            logger.exception(e)
            raise RuntimeError("failed to create worker instance") from e

        # SHUT DOWN OTHER WORKERS to enfroce single worker for now
        self._wapi.close_other_workers(self._worker_instance)

        # Store heartbeat length and override the HEARTBEAT_INTERVAL
        self._heartbeat_length = heartbeat_length
        self.HEARTBEAT_INTERVAL = timedelta(seconds=heartbeat_length)

        super().__init__(rabbitmq_connection)

        self._install_dir = install_directory
        # TODO - deprecated, fix
        self._threadpool = NamedThreadPool()

        self._servers_lock = Lock()
        self._servers: list[Server] = []

        self._futures = []

    def _send_heartbeat(self):
        self._wapi.worker_heartbeat(self._worker_instance)

    def _initialize_service(self):
        logger.info("noop")

    def _do_work(self, log_still_running: bool):
        # prune servers
        logger.debug("%s", log_still_running)
        self._servers_lock.acquire()
        new_server_list = []
        for server in self._servers:
            if server.is_shutdown:
                logger.info("%s is shutdown, pruning", server.instance)
                continue
            new_server_list.append(server)
        self._servers = new_server_list
        self._servers_lock.release()

    def _handle_commands(self, commands: list[Command]):
        for command in commands:
            if command.command_type == CommandType.START:
                self.__handle_start_command(command)
            elif command.command_type == CommandType.STOP:
                self.__handle_stop_command(command)
            elif command.command_type == CommandType.STDIN:
                self.__handle_stdin_command(command)
            else:
                logger.warning("unknown command for worker %s", command)

    def __handle_start_command(self, command: Command):
        # for now, just taking in the id from the arg list as-is until better typing TODO
        if len(command.command_args) != 1:
            logger.warning(
                "too many args, just want ID %s",
                command.command_args,
            )
            return
        self._servers_lock.acquire()
        game_server_config_id = int(command.command_args[0])

        can_start = True
        for server in self._servers:
            if server._config.game_server_config_id == game_server_config_id:
                logger.warning(
                    "server with app_id %s already running, ignoring create request",
                    server._config.game_server_config_id,
                )
                can_start = False
                break
        self._servers_lock.release()
        if can_start:
            self._create_server(game_server_config_id)
            # don't pass command through, doing creation in service layer for now

    def __handle_stop_command(self, command: Command):
        if len(command.command_args) == 0:
            # No arguments means shutdown the entire worker
            logger.info(
                "stop command with no args received, triggering worker shutdown"
            )
            self._trigger_internal_shutdown()
        elif len(command.command_args) == 1:
            # One argument means stop a specific game server instance
            game_server_config_id = int(command.command_args[0])
            self._servers_lock.acquire()
            for server in self._servers:
                if server._config.game_server_config_id == game_server_config_id:
                    logger.info("stopping server %s", server.instance)
                    # pass command through
                    server.execute_command(command)
            self._servers_lock.release()
        else:
            logger.warning(
                "stop command should have 0 args (worker shutdown) or 1 arg (server ID), got %s",
                command.command_args,
            )

    def __handle_stdin_command(self, command: Command):
        if len(command.command_args) < 1:
            logger.warning(
                "too few args, at least need ID %s",
                command.command_args,
            )
            return
        game_server_config_id = int(command.command_args[0])
        self._servers_lock.acquire()
        for server in self._servers:
            if server._config.game_server_config_id == game_server_config_id:
                logger.info("sending stdin to server %s", server.instance)
                # chain command
                server.execute_command(command)
                break
        self._servers_lock.release()

    def _shutdown(self):
        # Cascade shutdown to all dependent servers
        logger.info(
            "Initiating shutdown cascade to %d dependent servers", len(self._servers)
        )

        # Trigger shutdown for all servers
        self._servers_lock.acquire()
        servers_to_shutdown = list(self._servers)  # Create a copy to avoid lock issues
        self._servers_lock.release()

        for server in servers_to_shutdown:
            if not server.is_shutdown:
                logger.info("Triggering shutdown for server %s", server.instance)
                server._trigger_internal_shutdown()

        # Wait for all servers to complete shutdown
        logger.info("Waiting for dependent servers to complete shutdown")
        for server in servers_to_shutdown:
            # Wait with a reasonable timeout to avoid hanging indefinitely
            timeout_seconds = 30
            wait_count = 0
            while (
                not server.is_shutdown and wait_count < timeout_seconds * 10
            ):  # Check every 100ms
                time.sleep(0.1)
                wait_count += 1

            if server.is_shutdown:
                logger.info("Server %s shutdown completed", server.instance)
            else:
                logger.warning(
                    "Server %s shutdown timeout after %d seconds",
                    server.instance,
                    timeout_seconds,
                )

        logger.info(
            "All dependent servers shutdown complete, proceeding with worker shutdown"
        )
        self._wapi.worker_shutdown(self._worker_instance)

    def _create_server(self, game_server_config_id: int):
        config: GameServerConfig = self._wapi.game_server_config(game_server_config_id)

        # Temp check to prevent duplicates
        # ideally this will be a check against the database via api
        self._servers_lock.acquire()
        game_server_ids = {
            server._game_server.game_server_id for server in self._servers
        }
        if config.game_server_id in game_server_ids:
            logger.warning(
                "server with app_id %s already running, ignoring create request",
                config.game_server_id,
            )
            self._servers_lock.release()
            return
        self._servers_lock.release()

        server = Server(
            rabbitmq_connection=self._rabbitmq_connection,
            wapi=self._wapi,
            root_install_directory=self._install_dir,
            config=config,
            worker_id=self._worker_instance.worker_id,
        )
        try:
            future = self._threadpool.submit(
                server.run,
                name=server.instance.get_thread_name(),
                # TODO - set this to false when we get blocked by steamcmd
                # should_update=True,
            )
        except Exception as e:
            logger.exception("Failed to start server: %s", e)
            raise RuntimeError(
                f"Failed to submit server work {config.game_server_config_id}: {e}"
            ) from e
        # TODO - just make server responsible for its own thread management
        # TODO - does threadpool ever get too big with dead threads?
        # TODO - should I use a threadpool for this? I think I should move to explicit thread management
        self._futures.append(future)
        # TODO - not thread safe, but this is the only thread working on it for now
        self._servers_lock.acquire()
        self._servers.append(server)
        self._servers_lock.release()
