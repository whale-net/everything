import logging

# The application logic layer
from typing import Annotated, Optional

from fastapi import APIRouter, Depends, HTTPException

from manman.src.host.api.shared.injectors import (
    current_game_server_instances,
    current_worker,
    game_server_config_db_repository,
    worker_command_pub_service,
)

# TODO - make use of these
# from manman.src.repository.message.pub import CommandPubService
# from manman.src.repository.rabbitmq.publisher import RabbitPublisher
from manman.src.host.api.shared.models import (
    CurrentInstanceResponse,  # TODO - move this
    StdinCommandRequest,
)
from manman.src.models import (
    Command,
    CommandType,
    ExternalStatusInfo,
    GameServerConfig,
    GameServerInstance,
    Worker,
)
from manman.src.repository.database import GameServerConfigRepository, StatusRepository
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
    current_game_server_instance: Annotated[
        list[GameServerInstance], Depends(current_game_server_instances)
    ],
) -> CurrentInstanceResponse:
    """
    Get all active game server instances for the current worker.
    """
    return CurrentInstanceResponse.from_instances(current_game_server_instance)


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
