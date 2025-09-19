import logging
import time
from datetime import datetime, timedelta

from amqpstorm import Connection

from manman.src.models import (
    ExternalStatusInfo,
    StatusType,
)
from manman.src.repository.database import DatabaseRepository
from manman.src.repository.message.pub import ExternalStatusInfoPubService
from manman.src.repository.message.sub import (
    ExternalStatusSubService,
    InternalStatusSubService,
)
from manman.src.repository.rabbitmq.config import (
    BindingConfig,
    EntityRegistry,
    ExchangeRegistry,
    MessageTypeRegistry,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
)
from manman.src.repository.rabbitmq.publisher import (
    RabbitPublisher,
)
from manman.src.repository.rabbitmq.subscriber import (
    RabbitSubscriber,
)

logger = logging.getLogger(__name__)


class StatusEventProcessor:
    """
    Status Event Processor - consumes status messages from worker queues.

    This service subscribes to a generic status queue pattern and processes
    status updates from workers. Currently logs all received status information
    for debugging purposes.

    TODO - this module is a bit strange in that we ferry internal statuses to extenral to be logged
    whereas the true external statues are observed by the status processor and written before sendign to external queue
    ideally this is improved, but for now it works
    and external consumers should be able to subscribe to status.
    """

    __WORKER_KEY: RoutingKeyConfig = RoutingKeyConfig(
        entity=EntityRegistry.WORKER,
        identifier=TopicWildcard.ANY,
        type=MessageTypeRegistry.STATUS,
    )

    __GAME_SERVER_INSTANCE_KEY: RoutingKeyConfig = RoutingKeyConfig(
        entity=EntityRegistry.GAME_SERVER_INSTANCE,
        identifier=TopicWildcard.ANY,
        type=MessageTypeRegistry.STATUS,
    )

    __SUPPORTED_ROUTING_KEYS: list[RoutingKeyConfig] = [
        __WORKER_KEY,
        __GAME_SERVER_INSTANCE_KEY,
    ]

    def __build_internal_status_subscriber(self) -> InternalStatusSubService:
        binding_config = BindingConfig(
            exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
            routing_keys=StatusEventProcessor.__SUPPORTED_ROUTING_KEYS,
        )
        queue_config = QueueConfig(
            name="status-processor-internal-queue",
            durable=True,
            exclusive=False,
            auto_delete=False,
        )

        rmq = RabbitSubscriber(self._rabbitmq_connection, binding_config, queue_config)
        return InternalStatusSubService(rmq)

    def __build_external_status_publisher(
        self, key: RoutingKeyConfig
    ) -> ExternalStatusInfoPubService:
        binding_config = BindingConfig(
            exchange=ExchangeRegistry.EXTERNAL_SERVICE_EVENT,
            routing_keys=[key],
        )

        rmq = RabbitPublisher(self._rabbitmq_connection, binding_config)
        return ExternalStatusInfoPubService(rmq)

    def __build_external_status_subscriber(self) -> ExternalStatusSubService:
        binding_config = BindingConfig(
            exchange=ExchangeRegistry.EXTERNAL_SERVICE_EVENT,
            routing_keys=StatusEventProcessor.__SUPPORTED_ROUTING_KEYS,
        )
        queue_config = QueueConfig(
            name="status-processor-external-queue",
            durable=True,
            exclusive=False,
            auto_delete=False,
        )
        rmq = RabbitSubscriber(self._rabbitmq_connection, binding_config, queue_config)
        return ExternalStatusSubService(rmq)

    def __init__(self, rabbitmq_connection: Connection):
        self._rabbitmq_connection = rabbitmq_connection
        self._is_running = False

        # Initialize database repository
        # TODO status repository
        self._db_repository = DatabaseRepository()

        self._internal_status_subscriber = self.__build_internal_status_subscriber()
        self._external_worker_status_publisher = self.__build_external_status_publisher(
            StatusEventProcessor.__WORKER_KEY
        )
        self._external_gsi_status_publisher = self.__build_external_status_publisher(
            StatusEventProcessor.__GAME_SERVER_INSTANCE_KEY
        )
        self._external_status_subscriber = self.__build_external_status_subscriber()

        # "status-processor-external-queue"
        # "status-processor-internal-queue"

        logger.info("Status Event Processor initialized")

    def run(self):
        """Main event processing loop - consumes and processes status messages."""
        self._is_running = True
        loop_log_time = datetime.now()

        try:
            logger.info("Status Event Processor starting")
            while self._is_running:
                if datetime.now() - loop_log_time > timedelta(seconds=30):
                    logger.info("Status processor still running")
                    loop_log_time = datetime.now()

                # Process any available status messages
                self._process_internal_status_messages()

                # Check for stale worker heartbeats
                self._check_worker_heartbeats()

                # Process extenral status messages
                self._process_external_status_messages()

                # Small sleep to avoid busy waiting
                time.sleep(0.5)

        except KeyboardInterrupt:
            logger.info("Received shutdown signal")
        finally:
            self._shutdown()

    # TODO - check for workers that are lost and have an end date so we can crash them

    def _check_worker_heartbeats(self):
        """
        Check for stale worker heartbeats and mark them as lost if necessary.
        This sends a notification to the external queue if a worker is marked as lost.
        """
        try:
            # Get workers with stale heartbeats using the database repository
            stale_workers = self._db_repository.get_workers_with_stale_heartbeats()

            for worker, current_status in stale_workers:
                logger.warning(
                    "Worker %s heartbeat is stale (last: %s), marking as LOST from %s",
                    worker.worker_id,
                    worker.last_heartbeat,
                    current_status,
                )

                # Send worker lost notification
                self._send_worker_lost_notification(worker.worker_id)

                # Get all active game server instances for this worker
                active_instances = self._db_repository.get_active_game_server_instances(
                    worker.worker_id
                )

                # Send lost notifications for all active game server instances
                for instance in active_instances:
                    logger.warning(
                        "Marking game server instance %s as LOST due to worker %s being lost",
                        instance.game_server_instance_id,
                        worker.worker_id,
                    )
                    self._send_game_server_lost_notification(
                        instance.game_server_instance_id
                    )

        except Exception as e:
            logger.exception("Error checking worker heartbeats: %s", e)

    def _send_external_status(self, external_status_info: ExternalStatusInfo):
        if external_status_info.game_server_instance_id is not None:
            # Publish to game server instance exchange
            self._external_gsi_status_publisher.publish_external_status(
                external_status_info
            )
        if external_status_info.worker_id is not None:
            # Publish to worker exchange
            self._external_worker_status_publisher.publish_external_status(
                external_status_info
            )

    def _send_worker_lost_notification(self, worker_id: int):
        """Send a worker lost notification to external queue"""
        try:
            # Create a status message indicating the worker is lost
            lost_status = ExternalStatusInfo.create(
                class_name="StatusEventProcessor",
                status_type=StatusType.LOST,
                worker_id=worker_id,
            )

            self._send_external_status(lost_status)
            self._db_repository.write_external_status_to_database(lost_status)
            logger.info("Worker lost notification sent for worker %s", worker_id)

        except Exception as e:
            logger.exception(
                "Error sending worker lost notification for worker %s: %s", worker_id, e
            )

    def _send_game_server_lost_notification(self, game_server_instance_id: int):
        """Send a game server lost notification to external queue"""
        try:
            # Create a status message indicating the game server instance is lost
            lost_status = ExternalStatusInfo.create(
                class_name="StatusEventProcessor",
                status_type=StatusType.LOST,
                game_server_instance_id=game_server_instance_id,
            )

            self._send_external_status(lost_status)
            self._db_repository.write_external_status_to_database(lost_status)
            logger.info(
                "Game server lost notification sent for instance %s",
                game_server_instance_id,
            )

        except Exception as e:
            logger.exception(
                "Error sending game server lost notification for instance %s: %s",
                game_server_instance_id,
                e,
            )

    def _process_internal_status_messages(self):
        """
        Process available status messages from the internal queue
        and route them to the external queue.

        This is not the best way to do this, but it's the way it's being done for now.
        """
        try:
            internal_status_infos = (
                self._internal_status_subscriber.get_internal_statuses()
            )
            for internal_status_info in internal_status_infos:
                logger.info(
                    "Internal status update received: type=%s id=%s, status=%s, timestamp=%s",
                    internal_status_info.entity_type,
                    internal_status_info.identifier,
                    internal_status_info.status_type,
                    internal_status_info.as_of,
                )
                external_status_info = ExternalStatusInfo.create_from_internal(
                    internal_status_info
                )
                self._send_external_status(external_status_info)
                self._db_repository.write_external_status_to_database(
                    external_status_info
                )
        except Exception as e:
            logger.exception("Error processing status messages: %s", e)

    def _process_external_status_messages(self):
        """Process available status messages from the external queue."""
        try:
            external_status_infos = (
                self._external_status_subscriber.get_external_status_infos()
            )
            for external_status_info in external_status_infos:
                # do not write to database here, just consume it as an example
                logger.info(
                    "this is a sample processor for external status message %s",
                    external_status_info,
                )
        except Exception as e:
            logger.exception("Error processing external status messages: %s", e)

    def _shutdown(self):
        """Clean shutdown of the processor."""
        logger.info("Shutting down Status Event Processor")
        self._is_running = False
        logger.info("Status Event Processor shutdown complete")
