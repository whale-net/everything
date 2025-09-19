import logging
from typing import Annotated

from amqpstorm import Connection
from fastapi import APIRouter, Depends, HTTPException

from manman.src.exceptions import WorkerAlreadyClosedException
from manman.src.host.api.shared.injectors import rmq_conn
from manman.src.models import InternalStatusInfo, StatusType, Worker
from manman.src.repository.database import WorkerRepository
from manman.src.repository.message.pub import InternalStatusInfoPubService
from manman.src.repository.rabbitmq.config import (
    BindingConfig,
    EntityRegistry,
    ExchangeRegistry,
    MessageTypeRegistry,
    RoutingKeyConfig,
)
from manman.src.repository.rabbitmq.publisher import RabbitPublisher

logger = logging.getLogger(__name__)

# TODO - add authcz
# TODO - this should have a better prefix taht is different from the server api
router = APIRouter(
    prefix="/worker"
)  # , dependencies=[Depends(has_basic_worker_authz)])


# TODO - fix this
# @router.get("/health")
# async def health() -> str:
#     return "OK"


@router.post("/create")
async def worker_create() -> Worker:
    repository = WorkerRepository()
    return repository.create_worker()


@router.put("/shutdown")
async def worker_shutdown(instance: Worker) -> Worker:
    repository = WorkerRepository()
    try:
        result = repository.shutdown_worker(instance.worker_id)
        if result is None:
            raise HTTPException(status_code=404, detail="Worker not found")
        return result
    except WorkerAlreadyClosedException as e:
        raise HTTPException(
            status_code=409,
            detail=f"Worker {e.worker_id} was already closed on {e.end_date.isoformat()}. Shutdown rejected.",
        )


@router.put("/shutdown/other")
async def worker_shutdown_other(
    instance: Worker, rmq_connection: Annotated[Connection, Depends(rmq_conn)]
):
    repository = WorkerRepository()
    lost_workers = repository.close_other_workers(instance.worker_id)
    # terminate the lost workers
    # TBD if this ist her ighte way tod oit
    if len(lost_workers) > 0:
        for worker in lost_workers:
            logger.warning(f"Worker {worker.worker_id} has been lost")

        # Send COMPLETE status to worker status queue for each shutdown worker
        for worker in lost_workers:
            rmq_publisher = RabbitPublisher(
                connection=rmq_connection,
                binding_configs=BindingConfig(
                    exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
                    routing_keys=[
                        RoutingKeyConfig(
                            entity=EntityRegistry.WORKER,
                            identifier=str(worker.worker_id),
                            type=MessageTypeRegistry.STATUS,
                        )
                    ],
                ),
            )
            pub_svc = InternalStatusInfoPubService(rmq_publisher)

            pub_svc.publish_status(
                InternalStatusInfo.create(
                    entity_type=EntityRegistry.WORKER,
                    identifier=str(worker.worker_id),
                    status_type=StatusType.COMPLETE,
                )
            )


# heartbeat
@router.post("/heartbeat")
async def worker_heartbeat(instance: Worker):
    repository = WorkerRepository()
    try:
        result = repository.update_worker_heartbeat(instance.worker_id)
        return result
    except Exception as e:
        # Handle specific case where worker is already closed
        if isinstance(e, WorkerAlreadyClosedException):
            raise HTTPException(
                status_code=410,
                detail=f"Worker {e.worker_id} was already closed on {e.end_date.isoformat()}. Heartbeat rejected.",
            )

        # Handle all other exceptions with generic 400
        raise HTTPException(status_code=400, detail=str(e))
