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


class CommandDefaultWithCommand(ManManBase):
    """Command default with nested command details."""
    
    game_server_command_default_id: int
    game_server_command_id: int
    command_value: str
    description: Optional[str]
    is_visible: bool
    game_server_command: GameServerCommand


class ConfigCommandWithCommand(ManManBase):
    """Config command with nested command details."""
    
    game_server_config_command_id: int
    game_server_config_id: int
    game_server_command_id: int
    command_value: str
    description: Optional[str]
    is_visible: bool
    game_server_command: GameServerCommand


class InstanceDetailsResponseWithCommands(ManManBase):
    """Response containing instance details with available commands and nested command info."""

    instance: GameServerInstance
    config: GameServerConfig
    command_defaults: list[CommandDefaultWithCommand]
    config_commands: list[ConfigCommandWithCommand]


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


class InstanceHistoryItem(ManManBase):
    """Single instance with runtime information."""

    game_server_instance_id: int
    game_server_config_id: int
    created_date: str
    end_date: Optional[str]
    runtime_seconds: Optional[int]  # None if still running
    status: str  # "running" or "stopped"


class InstanceHistoryResponse(ManManBase):
    """Response containing instance history for a game server."""

    game_server_id: int
    instances: list[InstanceHistoryItem]


class CreateGameServerRequest(ManManBase):
    """Request to create a new game server."""

    name: str
    server_type: str
    app_id: int


class CreateGameServerCommandRequest(ManManBase):
    """Request to create a new game server command."""

    name: str
    command: str
    description: Optional[str] = None
    is_visible: bool = True
