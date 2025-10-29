from typing import Optional

from manman.src.models import (
    GameServerCommand,
    GameServerCommandDefaults,
    GameServerConfig,
    GameServerConfigCommands,
    GameServerInstance,
    ManManBase,
    Worker,
)


class StdinCommandRequest(ManManBase):
    """
    Request to send to the worker to start a game server instance.
    """

    commands: list[str]


class CurrentInstanceResponse(ManManBase):
    """
    Response to the worker to start a game server instance.
    """

    game_server_instances: list[GameServerInstance]
    workers: list[Worker]
    configs: list[GameServerConfig]

    @classmethod
    def from_instances(
        cls, instances: list[GameServerInstance]
    ) -> "CurrentInstanceResponse":
        workers = {instance.worker_id: instance.worker for instance in instances}
        configs = {
            instance.game_server_config_id: instance.game_server_config
            for instance in instances
        }
        return cls(
            game_server_instances=instances,
            workers=list(workers.values()),
            configs=list(configs.values()),
        )


class InstanceDetailsResponse(ManManBase):
    """Response containing instance details with available commands."""

    instance: GameServerInstance
    config: GameServerConfig
    command_defaults: list[GameServerCommandDefaults]
    config_commands: list[GameServerConfigCommands]


class ExecuteCommandRequest(ManManBase):
    """Request to execute a command on an instance."""

    command_type: str  # "default" or "config"
    command_id: int  # Either game_server_command_default_id or game_server_config_command_id
    custom_value: Optional[str] = None  # Optional override


class ExecuteCommandResponse(ManManBase):
    """Response after executing a command."""

    status: str
    message: str
    command: str


class CreateConfigCommandRequest(ManManBase):
    """Request to create a new config command."""

    game_server_command_id: int
    command_value: str
    description: Optional[str] = None
