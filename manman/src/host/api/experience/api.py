import logging

# The application logic layer
from typing import Annotated, Optional

from fastapi import APIRouter, Depends, HTTPException

from manman.src.host.api.shared.injectors import (
    current_game_server_instances,
    current_worker,
    game_server_config_db_repository,
    game_server_instance_db_repository,
    worker_command_pub_service,
)

# TODO - make use of these
# from manman.src.repository.message.pub import CommandPubService
# from manman.src.repository.rabbitmq.publisher import RabbitPublisher
from manman.src.host.api.shared.models import (
    CommandDefaultWithCommand,
    ConfigCommandWithCommand,
    CreateConfigCommandRequest,
    CreateGameServerCommandRequest,
    CreateGameServerRequest,
    CurrentInstanceResponse,  # TODO - move this
    ExecuteCommandRequest,
    ExecuteCommandResponse,
    InstanceDetailsResponseWithCommands,
    InstanceHistoryItem,
    InstanceHistoryResponse,
    StdinCommandRequest,
)
from manman.src.models import (
    Command,
    CommandType,
    ExternalStatusInfo,
    GameServer,
    GameServerCommand,
    GameServerConfig,
    GameServerConfigCommands,
    GameServerInstance,
    Worker,
)
from manman.src.repository.database import (
    GameServerConfigRepository,
    GameServerInstanceRepository,
    StatusRepository,
)
from manman.src.repository.message.pub import CommandPubService

router = APIRouter()

logger = logging.getLogger(__name__)


@router.get("/worker/current")
async def worker_current(
    current_worker: Annotated[Worker, Depends(current_worker)],
) -> Worker:
    return current_worker


@router.post("/worker/shutdown")
async def worker_shutdown(
    current_worker: Annotated[Worker, Depends(current_worker)],
    worker_command_pub_svc: Annotated[
        CommandPubService, Depends(worker_command_pub_service)
    ],
):
    """
    Shutdown the current worker.

    This endpoint sends a shutdown command to the current worker's command queue.
    The worker will gracefully shut down all running game server instances and
    terminate the worker service.

    :return: success response with worker ID
    """
    # Create a Command object with CommandType.STOP and no arguments
    command = Command(command_type=CommandType.STOP, command_args=[])
    worker_command_pub_svc.publish_command(command)

    return {
        "status": "success",
        "message": f"Shutdown command sent to worker {current_worker.worker_id}",
    }


@router.get("/worker/status")
async def get_worker_status(
    current_worker: Annotated[Worker, Depends(current_worker)],
) -> Optional[ExternalStatusInfo]:
    """
    Get the latest status information for the current worker.

    This queries the status repository to get the most recent status update
    from the worker, including heartbeat and health information.

    :return: Latest status info for the current worker, or None if no status exists
    """
    status_repo = StatusRepository()
    status = status_repo.get_latest_worker_status(current_worker.worker_id)
    if not status:
        raise HTTPException(status_code=404, detail="Worker status not found")
    return status


@router.get("/gameserver")
async def get_game_servers(
    game_server_config_repo: Annotated[
        GameServerConfigRepository, Depends(game_server_config_db_repository)
    ],
) -> list[GameServerConfig]:
    """
    Get all game server configs

    Although it seems strange for us to return configs instead of instances,
    this is the way the API is designed. We want to make the /gameserver/ endpoint
    the way you would interact with a game server. The whole instance thing
    should be abstracted away from the user.

    :return: list of game server configs
    """
    return game_server_config_repo.get_game_server_configs()


@router.get("/gameserver/current")
async def get_current_game_servers(
    current_game_server_instance: Annotated[
        list[GameServerInstance], Depends(current_game_server_instances)
    ],
) -> list[GameServerInstance]:
    """
    Get all currently running game server instances for the current worker.

    This returns the actual running instances, not configs.
    Useful for seeing what's actively running right now.

    :return: list of active game server instances
    """
    return current_game_server_instance


@router.get("/gameserver/{id}/status")
async def get_game_server_status(
    id: int,
    current_game_server_instance: Annotated[
        list[GameServerInstance], Depends(current_game_server_instances)
    ],
) -> Optional[ExternalStatusInfo]:
    """
    Get the latest status information for a game server by config ID.

    This finds the currently running instance for the given game server config ID
    and returns its most recent status information.

    :param id: game server config ID
    :return: Latest status info for the game server instance, or None if not running
    """
    # Find the instance for this config ID among currently running instances
    instance = next(
        (i for i in current_game_server_instance if i.game_server_config_id == id),
        None,
    )
    
    if not instance:
        raise HTTPException(
            status_code=404,
            detail=f"No running instance found for game server config {id}",
        )
    
    status_repo = StatusRepository()
    status = status_repo.get_latest_instance_status(instance.game_server_instance_id)
    
    if not status:
        raise HTTPException(
            status_code=404,
            detail=f"No status information found for instance {instance.game_server_instance_id}",
        )
    
    return status


@router.post("/gameserver/{id}/start")
async def start_game_server(
    id: int,
    current_worker: Annotated[Worker, Depends(current_worker)],
    worker_command_pub_svc: Annotated[
        CommandPubService, Depends(worker_command_pub_service)
    ],
):
    """
    Given the game server config ID, start a game server instance

    :param id: game server config ID
    :param channel: rabbitmq channel
    :return: arbitrary response
    """
    # FastAPI instrumentation automatically creates spans for this endpoint
    # Create a Command object with CommandType.START and game_server_config_id as arg
    command = Command(command_type=CommandType.START, command_args=[str(id)])
    worker_command_pub_svc.publish_command(command)

    # TODO - FUTURE enhancement, have worker echo the instance back to the host
    # could do json, or could lookup via session
    # the idea of having the worker hit the host for an instance
    # just to send it back to the host seems a bit funny
    # but is also effective because the workerdal is effectively its own
    # service layer https://www.youtube.com/watch?v=-FtCTW2rVFM
    return {
        "status": "success",
        "message": f"Start command sent to worker {current_worker.worker_id}",
    }


@router.post("/gameserver/{id}/stop")
async def stop_game_server(
    id: int,
    current_worker: Annotated[Worker, Depends(current_worker)],
    worker_command_pub_svc: Annotated[
        CommandPubService, Depends(worker_command_pub_service)
    ],
):
    """
    Given the game server config ID, stop a game server instance

    Finds the current worker, and sends a stop command to it
    It is up to the worker to handle the command
    and stop the game server instance.

    This endpoint provides an abstract gameserver interface
    to users, so they don't have to know about the worker
    and how it works

    :param id: game server config ID
    :param channel: rabbitmq channel
    :return: arbitrary response
    """
    # FastAPI instrumentation automatically creates spans for this endpoint
    command = Command(command_type=CommandType.STOP, command_args=[str(id)])
    worker_command_pub_svc.publish_command(command)

    return {
        "status": "success",
        "message": f"Stop command sent to worker {current_worker.worker_id}",
    }


@router.post("/gameserver/{id}/stdin")
async def stdin_game_server(
    id: int,
    current_worker: Annotated[Worker, Depends(current_worker)],
    worker_command_pub_svc: Annotated[
        CommandPubService, Depends(worker_command_pub_service)
    ],
    body: StdinCommandRequest,
):
    """
    Send a stdin command to the game server config's running instance

    This finds the current worker, and sends a stdin command to it
    It is up to the worker to handle the command
    and send it to the game server instance.

    This endpoint does not have a bheavior defined if no server is running.

    :param id: game server config ID
    :param channel: rabbitmq channel
    :param body: StdinCommandRequest
    :return: arbitrary response
    """
    # FastAPI instrumentation automatically creates spans for this endpoint
    command = Command(
        command_type=CommandType.STDIN, command_args=[str(id), *body.commands]
    )
    worker_command_pub_svc.publish_command(command)

    return {
        "status": "success",
        "message": f"Stdin command sent to worker {current_worker.worker_id}",
    }


@router.get("/gameserver/instances/active")
async def get_active_game_server_instances(
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
    current_worker: Annotated[Worker, Depends(current_worker)],
    include_crashed: bool = False,
) -> CurrentInstanceResponse:
    """
    Get all active game server instances for the current worker.
    
    Args:
        include_crashed: If True, also includes the last crashed instance for each game server config
    """
    instances = game_server_instance_repo.get_current_instances(
        current_worker.worker_id, include_crashed=include_crashed
    )
    return CurrentInstanceResponse.from_instances(instances)


@router.get("/gameserver/instance/{instance_id}")
async def get_instance_details(
    instance_id: int,
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
) -> InstanceDetailsResponseWithCommands:
    """
    Get detailed information about a specific game server instance including available commands.

    Args:
        instance_id: The game server instance ID

    Returns:
        Instance details with config, command defaults, and config-specific commands
    """
    result = game_server_instance_repo.get_instance_with_commands(instance_id)
    if not result:
        raise HTTPException(status_code=404, detail="Instance not found")

    instance, config, defaults, config_cmds = result
    
    # Convert to response models with nested command info
    command_defaults_with_cmds = [
        CommandDefaultWithCommand(
            game_server_command_default_id=d.game_server_command_default_id,
            game_server_command_id=d.game_server_command_id,
            command_value=d.command_value,
            description=d.description,
            is_visible=d.is_visible,
            game_server_command=d.game_server_command,
        )
        for d in defaults
    ]
    
    config_commands_with_cmds = [
        ConfigCommandWithCommand(
            game_server_config_command_id=c.game_server_config_command_id,
            game_server_config_id=c.game_server_config_id,
            game_server_command_id=c.game_server_command_id,
            command_value=c.command_value,
            description=c.description,
            is_visible=c.is_visible,
            game_server_command=c.game_server_command,
        )
        for c in config_cmds
    ]
    
    return InstanceDetailsResponseWithCommands(
        instance=instance,
        config=config,
        command_defaults=command_defaults_with_cmds,
        config_commands=config_commands_with_cmds,
    )


@router.post("/gameserver/instance/{instance_id}/command")
async def execute_instance_command(
    instance_id: int,
    body: ExecuteCommandRequest,
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
    worker_command_pub_svc: Annotated[
        CommandPubService, Depends(worker_command_pub_service)
    ],
) -> ExecuteCommandResponse:
    """
    Execute a command on a game server instance.

    Args:
        instance_id: The game server instance ID
        body: Command execution request

    Returns:
        Execution status and the resolved command string
    """
    from manman.src.models import GameServerCommandDefaults, StatusType

    # Get instance and validate it's active
    result = game_server_instance_repo.get_instance_with_commands(instance_id)
    if not result:
        raise HTTPException(status_code=404, detail="Instance not found")

    instance, config, defaults, config_cmds = result

    # Check instance is active (get latest status)
    status_repo = StatusRepository()
    latest_status = status_repo.get_latest_instance_status(instance_id)
    if latest_status and latest_status.status_type == StatusType.CRASHED:
        raise HTTPException(
            status_code=400,
            detail="Cannot execute command on crashed instance",
        )

    # Resolve the command based on type
    command_str = None
    description = None

    if body.command_type == "default":
        # Find the default command
        default_cmd = next(
            (d for d in defaults if d.game_server_command_default_id == body.command_id),
            None,
        )
        if not default_cmd:
            raise HTTPException(status_code=404, detail="Default command not found")

        command_value = body.custom_value or default_cmd.command_value
        command_str = default_cmd.game_server_command.command.replace(
            "{value}", command_value
        ).replace("{map}", command_value)
        description = default_cmd.description or default_cmd.game_server_command.description

    elif body.command_type == "config":
        # Find the config command
        config_cmd = next(
            (
                c
                for c in config_cmds
                if c.game_server_config_command_id == body.command_id
            ),
            None,
        )
        if not config_cmd:
            raise HTTPException(status_code=404, detail="Config command not found")

        command_value = body.custom_value or config_cmd.command_value
        command_str = config_cmd.game_server_command.command.replace(
            "{value}", command_value
        ).replace("{map}", command_value)
        description = config_cmd.description or config_cmd.game_server_command.description

    else:
        raise HTTPException(
            status_code=400, detail="Invalid command_type (must be 'default' or 'config')"
        )

    # Send command via existing stdin mechanism
    command = Command(
        command_type=CommandType.STDIN,
        command_args=[str(config.game_server_config_id), command_str],
    )
    worker_command_pub_svc.publish_command(command)

    return ExecuteCommandResponse(
        status="success",
        message=f"Command sent to instance {instance_id}",
        command=command_str,
    )


@router.get("/gameserver/{game_server_id}/commands")
async def get_available_commands(
    game_server_id: int,
    game_server_config_repo: Annotated[
        GameServerConfigRepository, Depends(game_server_config_db_repository)
    ],
) -> list[GameServerCommand]:
    """
    Get all available commands for a game server.

    Args:
        game_server_id: The game server ID

    Returns:
        List of available commands
    """
    return game_server_config_repo.get_commands_for_game_server(game_server_id)


@router.post("/gameserver/config/{config_id}/command")
async def create_config_command(
    config_id: int,
    body: CreateConfigCommandRequest,
    game_server_config_repo: Annotated[
        GameServerConfigRepository, Depends(game_server_config_db_repository)
    ],
) -> GameServerConfigCommands:
    """
    Create a new config-specific command.

    Args:
        config_id: The game server config ID
        body: Command creation request

    Returns:
        The created config command
    """
    try:
        return game_server_config_repo.create_config_command(
            config_id=config_id,
            command_id=body.game_server_command_id,
            command_value=body.command_value,
            description=body.description,
        )
    except Exception as e:
        # Handle duplicate command errors
        if "unique constraint" in str(e).lower():
            raise HTTPException(
                status_code=409,
                detail="Command with this value already exists for this config",
            )
        raise


# @router.post("/gameserver/instance/{id}/stdin")
# async def stdin_game_server_instance(
#     id: int,
#     gsi_pub_cmd_svc: Annotated[Channel, Depends(game_server_instance_command_pub_service)],
#     body: StdinCommandRequest,
# ):
#     """
#     Send a stdin command to the game server instance

#     This sends a command directly to the game server instance.
#     The worker is not involved in this process.

#     This endpoint does not have a behavior defined if the game server instance is not running.

#     :param id: game server instance ID
#     :param channel: rabbitmq channel
#     :param body: StdinCommandRequest
#     :return: arbitrary response
#     """
#     # Copy from above, but send to instance
#     # first I think I need to make the instance handle the command though
#     raise NotImplementedError("Not implemented yet")


@router.get("/gameserver/{game_server_id}/instances")
async def get_game_server_instance_history(
    game_server_id: int,
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
    limit: int = 10,
):
    """
    Get instance history for a game server with runtime calculations.

    Args:
        game_server_id: The game server ID
        limit: Maximum number of instances to return (default 10)

    Returns:
        Instance history with runtime information
    """
    from datetime import datetime, timezone

    instances = game_server_instance_repo.get_instance_history(game_server_id, limit)

    history_items = []
    for inst in instances:
        if inst.end_date:
            runtime_seconds = int((inst.end_date - inst.created_date).total_seconds())
            status = "stopped"
            end_date_str = inst.end_date.isoformat()
        else:
            runtime_seconds = int(
                (datetime.now(timezone.utc) - inst.created_date).total_seconds()
            )
            status = "running"
            end_date_str = None

        history_items.append(
            InstanceHistoryItem(
                game_server_instance_id=inst.game_server_instance_id,
                game_server_config_id=inst.game_server_config_id,
                created_date=inst.created_date.isoformat(),
                end_date=end_date_str,
                runtime_seconds=runtime_seconds,
                status=status,
            )
        )

    return InstanceHistoryResponse(
        game_server_id=game_server_id, instances=history_items
    )


@router.get("/gameserver/types")
async def list_game_servers(
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
) -> list[GameServer]:
    """Get all game server types."""
    return game_server_instance_repo.list_game_servers()


@router.post("/gameserver/types")
async def create_game_server(
    body: CreateGameServerRequest,
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
) -> GameServer:
    """Create a new game server type."""
    try:
        return game_server_instance_repo.create_game_server(
            name=body.name, server_type=body.server_type, app_id=body.app_id
        )
    except Exception as e:
        if "unique constraint" in str(e).lower():
            raise HTTPException(
                status_code=409, detail="Game server with this name already exists"
            )
        raise


@router.post("/gameserver/types/{game_server_id}/command")
async def create_game_server_command(
    game_server_id: int,
    body: CreateGameServerCommandRequest,
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
) -> GameServerCommand:
    """Create a new command for a game server type."""
    try:
        return game_server_instance_repo.create_game_server_command(
            game_server_id=game_server_id,
            name=body.name,
            command=body.command,
            description=body.description,
            is_visible=body.is_visible,
        )
    except Exception as e:
        if "unique constraint" in str(e).lower():
            raise HTTPException(
                status_code=409,
                detail="Command with this name already exists for this game server",
            )
        raise
