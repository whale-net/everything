# The status API - read-only queries for status information

from fastapi import APIRouter, HTTPException

from manman.models import ExternalStatusInfo
from manman.repository.database import StatusRepository

router = APIRouter(prefix="/status")


# status for a worker id
@router.get(
    "/worker/{worker_id}",
)
async def get_worker_status(
    worker_id: int,
) -> ExternalStatusInfo:
    repository = StatusRepository()
    status = repository.get_latest_worker_status(worker_id)
    if not status:
        raise HTTPException(status_code=404, detail="Worker not found")
    return status


# status for a single game server instance
@router.get(
    "/instance/{game_server_instance_id}",
)
async def get_game_server_instance(
    game_server_instance_id: int,
) -> ExternalStatusInfo:
    repository = StatusRepository()
    status = repository.get_latest_instance_status(game_server_instance_id)
    if status is None:
        raise HTTPException(status_code=404, detail="Instance not found")
    return status
