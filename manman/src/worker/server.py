import logging
import os

from amqpstorm import Connection

from manman.models import (
    Command,
    CommandType,
    GameServerConfig,
    GameServerInstance,
    ServerType,
)
from manman.repository.api_client import WorkerAPIClient
from manman.repository.rabbitmq.config import EntityRegistry
from manman.util import env_list_to_dict
from manman.worker.abstract_service import ManManService
from manman.worker.processbuilder import ProcessBuilder, ProcessBuilderStatus
from manman.worker.steamcmd import SteamCMD

logger = logging.getLogger(__name__)


class Server(ManManService):
    @property
    def service_entity_type(self) -> EntityRegistry:
        return EntityRegistry.GAME_SERVER_INSTANCE

    @property
    def identifier(self) -> str:
        if not hasattr(self, "_instance"):
            raise RuntimeError("Server instance not initialized")
        if not hasattr(self._instance, "game_server_instance_id"):
            raise RuntimeError(
                "Server instance does not have a game_server_instance_id"
            )
        if self._instance.game_server_instance_id is None:
            raise RuntimeError("Server instance game_server_instance_id is None")
        # Return the game server instance ID as a string
        return str(self._instance.game_server_instance_id)

    @property
    def instance(self) -> GameServerInstance:
        return self._instance

    @property
    def is_shutdown(self) -> bool:
        return self._instance.end_date is not None

    def __init__(
        self,
        rabbitmq_connection: Connection,
        *,
        wapi: WorkerAPIClient,
        root_install_directory: str,
        config: GameServerConfig,
        worker_id: int,
    ) -> None:
        # pre-init
        self._wapi = wapi
        self._worker_id = worker_id
        self._config = config
        self._instance = self._wapi.game_server_instance_create(
            self._config, self._worker_id
        )

        # Initialize the ManManService base class
        super().__init__(rabbitmq_connection)

        self._game_server = self._wapi.game_server(self._config.game_server_id)
        logger.info("starting instance %s", self._instance.model_dump_json())

        self._root_install_directory = root_install_directory
        self._server_directory = os.path.join(
            self._root_install_directory,
            # game_server is unique on server_type_appid
            ServerType(self._game_server.server_type).name.lower(),
            str(self._game_server.app_id),
            # and then config is unique on game_server_id, name
            self._config.name,
        )

        executable_path = os.path.join(self._server_directory, self._config.executable)
        pb = ProcessBuilder(executable=executable_path)
        for arg in self._config.args:
            pb.add_parameter(arg)
        self._proc = pb
        logger.info("ProcessBuilder initialized with executable: %s", executable_path)

    def _send_heartbeat(self):
        """Send a heartbeat for the game server instance."""
        self._wapi.server_heartbeat(self._instance)

    def _initialize_service(self):
        """Initialize the server service - install SteamCMD."""
        logger.info(
            "Initializing server service for instance %s",
            self._instance.game_server_instance_id,
        )

        # Install game server via SteamCMD
        steam = SteamCMD(self._server_directory)
        try:
            steam.install(app_id=self._game_server.app_id)
        except Exception as e:
            logger.exception("Failed to install game server: %s", e)
            raise RuntimeError(
                f"Failed to install game server {self._game_server.app_id}: {e}"
            ) from e

        # Start the game server process
        try:
            # TODO - temp workaround for env var, need to come from config
            extra_env = env_list_to_dict(self._config.env_var)
            logger.info("extra_env=%s", extra_env)
            self._proc.run(extra_env=extra_env)
        except Exception as e:
            logger.exception(e)
            raise RuntimeError(
                f"Failed to start game server process for {self._game_server.app_id}: {e}"
            ) from e

    def _do_work(self, log_still_running: bool):
        """Main work loop - read process output and check status."""
        # Read process output
        if hasattr(self, "_proc"):
            self._proc.read_output()

            # Check if process has stopped
            status = self._proc.status
            if status in (ProcessBuilderStatus.STOPPED, ProcessBuilderStatus.FAILED):
                logger.info("Process has stopped, initiating shutdown")
                self._trigger_internal_shutdown()

    def _handle_commands(self, commands: list[Command]):
        """Handle incoming commands."""
        for command in commands:
            self.execute_command(command)

    def execute_command(self, command: Command) -> None:
        # start commands can't be handled here, they are handled by the worker service
        # but perhaps oneday it could be re-used for a restart?
        if command.command_type == CommandType.STDIN:
            self.__handle_stdin_command(command)
        elif command.command_type == CommandType.STOP:
            self.__handle_stop_command()
        else:
            logger.warning(
                "unsupported command type: %s for %s",
                command.command_type,
                self.__class__.__name__,
            )

    def _shutdown(self):
        """Shutdown the server service."""
        # TODO kill
        # TODO - move the prevent double shutdown check to the base class
        if not self.is_shutdown:
            logger.info(
                "shutting down instance %s", self._instance.game_server_instance_id
            )

            # Capture instance ID before shutdown for status publishing
            instance_id = self._instance.game_server_instance_id

            if hasattr(self, "_proc"):
                self._proc.stop()
            self._instance = self._wapi.game_server_instance_shutdown(self._instance)

            logger.info(
                "shutdown complete for instance %s",
                instance_id,
            )

    def __handle_stop_command(self) -> None:
        logger.info(
            "stop command received for instance %s",
            self._instance.game_server_instance_id,
        )
        self._trigger_internal_shutdown()

    def __handle_stdin_command(self, command: Command) -> None:
        if len(command.command_args) < 2:
            logger.warning(
                "too few args, need config ID and commands %s",
                command.command_args,
            )
            return

        # for now going to merge them all together blindly
        stdin_command = " ".join(command.command_args[1:])
        logger.info(
            "stdin command received for instance %s: %s",
            self._instance.game_server_instance_id,
            stdin_command,
        )
        self._proc.write_stdin(stdin_command)
