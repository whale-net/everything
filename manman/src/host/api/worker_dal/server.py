from fastapi import APIRouter, HTTPException

from manman.src.exceptions import GameServerInstanceAlreadyClosedException
from manman.src.models import GameServer, GameServerConfig, GameServerInstance
from manman.src.repository.database import (
    GameServerInstanceRepository,
    GameServerRepository,
)

# TODO - add authcz
# TODO - this should have a better prefix taht is different from the worker api
router = APIRouter(
    prefix="/server"
)  # , dependencies=[Depends(has_basic_worker_authz)])


@router.post("/instance/create")
async def server_instance_create(body: GameServerInstance) -> GameServerInstance:
    repository = GameServerInstanceRepository()
    return repository.create_instance(body.game_server_config_id, body.worker_id)


@router.put("/instance/shutdown")
async def server_instance_shutdown(instance: GameServerInstance) -> GameServerInstance:
    repository = GameServerInstanceRepository()
    try:
        result = repository.shutdown_instance(instance.game_server_instance_id)
        if result is None:
            raise HTTPException(status_code=404, detail="Instance not found")
        return result
    except GameServerInstanceAlreadyClosedException as e:
        raise HTTPException(
            status_code=409,
            detail=f"Game server instance {e.instance_id} was already closed on {e.end_date.isoformat()}. Shutdown rejected.",
        )


@router.get("/instance/{id}")
async def server_instance(id: int) -> GameServerInstance:
    repository = GameServerInstanceRepository()
    instance = repository.get_instance_by_id(id)
    if instance is None:
        raise HTTPException(status_code=404, detail="Instance not found")
    return instance


@router.post("/instance/heartbeat/{id}")
async def server_instance_heartbeat(id: int) -> GameServerInstance:
    repository = GameServerInstanceRepository()
    instance = repository.update_instance_heartbeat(id)
    if instance is None:
        raise HTTPException(status_code=404, detail="Instance not found")
    return instance


@router.get("/config/{id}")
async def server_config(id: int) -> GameServerConfig:
    repository = GameServerRepository()
    config = repository.get_game_server_config_by_id(id)
    if config is None:
        raise HTTPException(status_code=404, detail="Config not found")
    return config


@router.get("/{id}")
async def server(id: int) -> GameServer:
    repository = GameServerRepository()
    server = repository.get_game_server_by_id(id)
    if server is None:
        raise HTTPException(status_code=404, detail="Server not found")
    return server
