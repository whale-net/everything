import logging
from typing import Annotated, AsyncGenerator

from amqpstorm import Channel, Connection
from fastapi import Depends, Header, HTTPException
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
from manman.src.util import get_auth_api_client, get_sqlalchemy_session
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


async def sql_session() -> SQLSession:
    """
    Dependency to inject a SQLAlchemy/SQLModel session.

    Wraps my own function to decouple my weird implementation from FastAPI's dependency injection system.
    """
    return get_sqlalchemy_session()


async def worker_db_repository(
    session: Annotated[SQLSession, Depends(sql_session)],
) -> WorkerRepository:
    """
    Dependency to inject a WorkerRepository.
    This repository is used to interact with the worker database.
    """
    return WorkerRepository(session=session)


async def game_server_instance_db_repository(
    session: Annotated[SQLSession, Depends(sql_session)],
) -> GameServerInstanceRepository:
    """
    Dependency to inject a GameServerInstanceRepository.
    This repository is used to interact with game server instances in the database.
    """
    return GameServerInstanceRepository(session=session)


async def game_server_config_db_repository(
    session: Annotated[SQLSession, Depends(sql_session)],
) -> GameServerConfigRepository:
    """
    Dependency to inject a GameServerInstanceRepository for game server configs.
    This repository is used to interact with game server configurations in the database.
    """
    return GameServerConfigRepository(session=session)


async def current_worker(
    worker_repo: Annotated[WorkerRepository, Depends(worker_db_repository)],
) -> Worker:
    """
    Dependency to inject the current worker.
    This worker is determined by the worker ID in the request context.
    """
    worker = worker_repo.get_current_worker()
    if not worker:
        raise HTTPException(status_code=404, detail="Worker not found")
    return worker


async def current_game_server_instances(
    game_server_instance_repo: Annotated[
        GameServerInstanceRepository, Depends(game_server_instance_db_repository)
    ],
    current_worker: Annotated[Worker, Depends(current_worker)],
) -> list[GameServerInstance]:
    """
    Dependency to inject the current game server instances for the worker.
    This is used to get the game server instances that are currently running on the worker.
    """
    instances = game_server_instance_repo.get_current_instances(
        current_worker.worker_id
    )
    # allow empty lists
    # if not instances:
    #     raise HTTPException(status_code=404, detail="No game server instances found for this worker")
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
