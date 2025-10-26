import logging
from typing import Annotated, AsyncGenerator

from amqpstorm import Channel, Connection
from fastapi import Depends, Header, HTTPException
from sqlalchemy.ext.asyncio import AsyncSession
from sqlmodel import Session as SQLSession

from manman.src.models import GameServerInstance, Worker
from manman.src.repository.api_client import AccessToken
from manman.src.repository.database import (
    GameServerConfigRepository,
    GameServerInstanceRepository,
    WorkerRepository,
)
from manman.src.repository.message.pub import CommandPubService
from manman.src.constants import EntityRegistry
from libs.python.rmq import (
    BindingConfig,
    ExchangeRegistry,
    MessageTypeRegistry,
    RabbitPublisher,
    RoutingKeyConfig,
)
from manman.src.util import get_async_session, get_auth_api_client, get_sqlalchemy_session
from libs.python.rmq import get_rabbitmq_connection

logger = logging.getLogger(__name__)


# using builtin fastapi classes is not helpful because my token provider endpoint is elsewhere
# and not handled by fastapi whatsoever
# there doesn't seem to be a way to handle that in a reasonable way
async def get_access_token(authorization: Annotated[str, Header()]) -> AccessToken:
    # print(authorization)
    # return

    if not (authorization.startswith("bearer ") or authorization.startswith("bearer ")):
        raise RuntimeError("bearer token not found")

    api_client = get_auth_api_client()
    token = api_client.create_token_from_str(authorization[7:])
    if not token.is_valid():
        raise RuntimeError("token invalid")
    if token.is_expired():
        raise RuntimeError("token expired")
    return token


async def has_basic_worker_authz(
    token: Annotated[AccessToken, Depends(get_access_token)],
):
    if "manman-worker" not in token.roles:
        raise HTTPException(status_code=401, detail="access token missing proper role")


# Sync session dependency for endpoints that haven't been migrated to async yet
async def sql_session() -> SQLSession:
    """
    Dependency to inject a sync SQLAlchemy/SQLModel session.
    
    NOTE: This is kept for backward compatibility with endpoints not yet migrated to async.
    New code should use get_async_session instead.
    """
    return get_sqlalchemy_session()


async def game_server_config_db_repository(
    session: Annotated[SQLSession, Depends(sql_session)],
) -> GameServerConfigRepository:
    """
    Dependency to inject a GameServerConfigRepository (sync version).
    """
    return GameServerConfigRepository(session=session)


async def current_worker(
    session: Annotated[AsyncSession, Depends(get_async_session)],
) -> Worker:
    """
    Dependency to inject the current worker using async database operations.
    
    This properly uses AsyncSession to avoid blocking the event loop,
    preventing gunicorn worker timeouts under load.
    """
    worker_repo = WorkerRepository(session=session)
    worker = await worker_repo.get_current_worker_async(session)
    if not worker:
        raise HTTPException(status_code=404, detail="Worker not found")
    return worker


async def current_game_server_instances(
    session: Annotated[SQLSession, Depends(sql_session)],
    current_worker: Annotated[Worker, Depends(current_worker)],
) -> list[GameServerInstance]:
    """
    Dependency to inject the current game server instances for the worker.
    
    NOTE: Uses sync session since get_current_instances hasn't been migrated to async yet.
    This is fine since this endpoint is called less frequently than current_worker.
    """
    game_server_instance_repo = GameServerInstanceRepository(session=session)
    instances = game_server_instance_repo.get_current_instances(
        current_worker.worker_id
    )
    return instances


async def rmq_conn() -> Connection:
    """
    Dependency to inject the RabbitMQ connection.

    Returns the persistent connection for this worker process.
    The connection is created once per worker and reused.
    """
    return get_rabbitmq_connection()


async def rmq_chan(
    connection: Annotated[Connection, Depends(rmq_conn)],
) -> AsyncGenerator[Channel, None]:
    """
    Dependency to inject a RabbitMQ channel.

    Creates a fresh channel per request from the persistent connection.
    Channels are lightweight and designed for per-operation use.
    """
    channel = connection.channel()
    try:
        yield channel
    finally:
        # Ensure channel is properly closed after use
        try:
            channel.close()
        except Exception as e:
            logger.warning("Error closing RabbitMQ channel: %s", e)


async def current_worker_routing_config(
    current_worker: Annotated[Worker, Depends(current_worker)],
) -> RoutingKeyConfig:
    """
    Creates a routing key configuration for a worker based on its ID.
    This is used to route messages to the correct worker.
    """
    return RoutingKeyConfig(
        entity=EntityRegistry.WORKER,
        identifier=str(current_worker.worker_id),
        type=MessageTypeRegistry.COMMAND,
    )


async def rmq_worker_publisher(
    rmq_conn: Annotated[Connection, Depends(rmq_conn)],
    worker_routing_key: Annotated[
        RoutingKeyConfig, Depends(current_worker_routing_config)
    ],
) -> RabbitPublisher:
    return RabbitPublisher(
        connection=rmq_conn,
        binding_configs=[
            BindingConfig(
                exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
                routing_keys=[worker_routing_key],
            )
        ],
    )


async def worker_command_pub_service(
    rmq_publisher: Annotated[RabbitPublisher, Depends(rmq_worker_publisher)],
) -> CommandPubService:
    """
    Dependency to inject a RabbitMQ publisher for worker commands.
    This publisher is used to send commands to the worker.
    """

    return CommandPubService(rmq_publisher)


# async def rmq_game_server_instance_publisher(
#     rmq_conn: Annotated[Connection, Depends(rmq_conn)],
#     game_server_instance_routing_key: Annotated[RoutingKeyConfig, Depends(game_server_instance_routing_config)]
# ) -> RabbitPublisher:
#     return RabbitPublisher(
#         connection=rmq_conn,
#         exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
#         routing_key=[game_server_instance_routing_key]
#     )

# async def game_server_instance_command_pub_service(
#     rmq_publisher: Annotated[RabbitPublisher, Depends(rmq_game_server_instance_publisher)]
# ) -> CommandPubService:
#     """
#     Dependency to inject a RabbitMQ publisher for game server instance commands.
#     This publisher is used to send commands to the game server instance.
#     """

#     return CommandPubService(rmq_publisher)
