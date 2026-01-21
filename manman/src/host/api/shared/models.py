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
    """Request to send commands to a worker's game server instance."""

    commands: list[str]


class CurrentInstanceResponse(ManManBase):
    """Response containing current game server instances with denormalized data."""

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
    """Response containing instance details with available commands.
    
    Returns flattened command data - UI can join with command details if needed.
    """

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
