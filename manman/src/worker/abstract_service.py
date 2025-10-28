import logging
import time
from abc import ABC, abstractmethod
from datetime import datetime, timedelta, timezone

from amqpstorm import Connection

from manman.src.models import Command, InternalStatusInfo, StatusType
from manman.src.repository.message.pub import (
    InternalStatusInfoPubService,
)
from manman.src.repository.message.sub import CommandSubService
from manman.src.constants import EntityRegistry
from libs.python.rmq import (
    BindingConfig,
    ExchangeRegistry,
    MessageTypeRegistry,
    QueueConfig,
    RabbitPublisher,
    RabbitSubscriber,
    RoutingKeyConfig,
)

logger = logging.getLogger(__name__)


class ManManService(ABC):
    """
    Abstract base class services with lifecycle and command consumption capabilities.
    """

    RMQ_EXCHANGE: ExchangeRegistry = ExchangeRegistry.INTERNAL_SERVICE_EVENT

    # this is surely too short, but it is just a heartbeat
    HEARTBEAT_INTERVAL: timedelta = timedelta(seconds=2)
    RUN_LOOP_INTERVAL: timedelta = timedelta(milliseconds=100)
    LOG_LOOP_INTERVAL: timedelta = RUN_LOOP_INTERVAL * 300

    @property
    @abstractmethod
    def service_entity_type(self) -> EntityRegistry:
        """
        Get the entity type for the service.
        """
        raise NotImplementedError(
            "Subclasses must implement the 'service_entity_type' property."
        )

    @property
    @abstractmethod
    def identifier(self) -> str:
        """
        Get the identifier for the service.

        NOTE: this will be called in the constructor, so it must be defined before the constructor is called.

        :return: The identifier string.
        """
        raise NotImplementedError(
            "Subclasses must implement the 'identifier' property."
        )

    @property
    def status_routing_key(self) -> RoutingKeyConfig:
        """
        Generate the routing key for status messages.
        """
        return RoutingKeyConfig(
            entity=self.service_entity_type,
            identifier=self.identifier,
            type=MessageTypeRegistry.STATUS,
        )

    @property
    def command_routing_key(self) -> RoutingKeyConfig:
        """
        Generate the routing key for command messages.
        """
        return RoutingKeyConfig(
            entity=self.service_entity_type,
            identifier=self.identifier,
            type=MessageTypeRegistry.COMMAND,
        )

    @property
    def command_queue_config(self) -> QueueConfig:
        """
        Generate the queue configuration for command messages.
        """
        return QueueConfig(
            name=f"dev-queue-name-{self.service_entity_type.name}-{self.identifier}",
            durable=True,
            exclusive=False,
            auto_delete=True,
        )

    def __build_status_publisher(self) -> InternalStatusInfoPubService:
        status_binding = BindingConfig(
            exchange=self.RMQ_EXCHANGE,
            routing_keys=[
                self.status_routing_key.build_key(),
            ],
        )
        rabbit_publisher = RabbitPublisher(
            connection=self._rabbitmq_connection,
            binding_configs=status_binding,
        )
        return InternalStatusInfoPubService(publisher=rabbit_publisher)

    def __build_command_consumer(self) -> CommandSubService:
        command_binding = BindingConfig(
            exchange=self.RMQ_EXCHANGE,
            routing_keys=[
                self.command_routing_key.build_key(),
            ],
        )
        rabbit_subscriber = RabbitSubscriber(
            connection=self._rabbitmq_connection,
            binding_configs=command_binding,
            queue_config=self.command_queue_config,
        )
        return CommandSubService(subscriber=rabbit_subscriber)

    def __create_internal_status_info(
        self,
        status_type: StatusType,
    ) -> InternalStatusInfo:
        """
        Create an InternalStatusInfo object for the service.

        :param status_type: The status type to set.
        :return: An InternalStatusInfo object with the service name and status type.
        """
        return InternalStatusInfo.create(
            entity_type=self.service_entity_type,
            identifier=self.identifier,
            status_type=status_type,
        )

    def __init__(self, rabbitmq_connection: Connection, **kwargs) -> None:
        """
        Initialize the service with a RabbitMQ connection.

        :param rabbitmq_connection: An AMQPStorm connection to the RabbitMQ server.
        """

        if len(kwargs) > 0:
            logger.warning(
                "Unused keyword arguments in %s: %s",
                self.__class__.__name__,
                ", ".join(kwargs.keys()),
            )

        logger.info("Creating service: %s", self.__class__.__name__)
        self.__is_stopped = False

        self._rabbitmq_connection = rabbitmq_connection
        self._status_pub_service = self.__build_status_publisher()
        self._command_sub_service = self.__build_command_consumer()

        self._status_pub_service.publish_status(
            internal_status=self.__create_internal_status_info(StatusType.CREATED),
        )

    @abstractmethod
    def _send_heartbeat(self):
        """
        Send a heartbeat message to indicate the service is running.
        """
        pass

    @abstractmethod
    def _initialize_service(self):
        """
        Initialize the service.

        Do extra non-init setup here, such as installing service runtime dependencies.
        Things that may be blocking that you didn't want to do in the constructor.
        """
        pass

    @abstractmethod
    def _do_work(self, log_still_running: bool):
        """
        Service unit of work
        """
        pass

    @abstractmethod
    def _handle_commands(self, commands: list[Command]):
        """
        Handle commands received from the command queue.

        This is not generic service stuff, and could be put in the do_work method,
        but this is used for everything so far, so it is abstracted here.
        Potentially makes it possible to have this be done in a separate thread
        or process if needed too.
        """
        pass

    @abstractmethod
    def _shutdown(self):
        """
        Shutdown the service gracefully.
        """
        pass

    def _trigger_internal_shutdown(self):
        """
        Trigger an internal shutdown of the service.

        This method is called to stop the service gracefully.
        It sets the internal flag to indicate the service should stop.
        """
        logger.info(
            "Triggering internal shutdown for service: %s", self.__class__.__name__
        )
        self.__is_stopped = True

    def run(self):
        """
        Run the service.

        This is the main entry point for the service lifecycle.
        It initializes the service, starts it, and handles any necessary setup.
        """

        try:
            logger.info("Initializing service: %s", self.__class__.__name__)
            self._status_pub_service.publish_status(
                internal_status=self.__create_internal_status_info(
                    StatusType.INITIALIZING
                ),
            )
            self._initialize_service()
            logger.info("Service %s initialized successfully", self.__class__.__name__)
        except Exception as e:
            logger.exception(
                "Error during initialization of service %s: %s",
                self.__class__.__name__,
                e,
            )
            self._status_pub_service.publish_status(
                internal_status=self.__create_internal_status_info(
                    StatusType.CRASHED
                ),
            )
            raise RuntimeError(
                f"Failed to initialize service {self.__class__.__name__}: {e}"
            ) from e

        try:
            logger.info("about to start service: %s", self.__class__.__name__)
            # TODO - this gives no time for the service to start running
            # this will be problematic for game servers that take a while to start
            self._status_pub_service.publish_status(
                internal_status=self.__create_internal_status_info(StatusType.RUNNING),
            )
            self._run()
            logger.info("Service %s has completed running", self.__class__.__name__)
        except Exception as e:
            logger.exception(
                "Error during execution of service %s: %s", self.__class__.__name__, e
            )
            raise RuntimeError(
                f"Failed to run service {self.__class__.__name__}: {e}"
            ) from e
        finally:
            try:
                # TODO - prevent double shutdown
                # once run is complete, start shutdown
                logger.info("Shutting down service: %s", self.__class__.__name__)
                self._shutdown()
            except Exception as e:
                logger.exception(
                    "Error during shutdown of service %s: %s",
                    self.__class__.__name__,
                    e,
                )
                raise RuntimeError(
                    f"Failed to shutdown service {self.__class__.__name__}: {e}"
                ) from e
            finally:
                self._status_pub_service.publish_status(
                    internal_status=self.__create_internal_status_info(
                        StatusType.COMPLETE
                    ),
                )

    def _run(self):
        """
        Contain main run loop logic for the service.
        """
        loop_log_time = datetime.now(timezone.utc) - self.LOG_LOOP_INTERVAL
        loop_heartbeat_time = datetime.now(timezone.utc) - self.HEARTBEAT_INTERVAL
        while not self.__is_stopped:
            # timing
            run_loop_start_time = datetime.now(timezone.utc)
            log_still_running = (
                run_loop_start_time - loop_log_time
            ) > self.LOG_LOOP_INTERVAL
            if log_still_running:
                logger.info("Service %s is running", self.__class__.__name__)
                loop_log_time = run_loop_start_time
            if (run_loop_start_time - loop_heartbeat_time) > self.HEARTBEAT_INTERVAL:
                self._send_heartbeat()
                loop_heartbeat_time = run_loop_start_time

            # Perform the main work of the service
            self._do_work(log_still_running=log_still_running)

            # Handle any commands received from the command queue
            commands = self._command_sub_service.get_commands()
            self._handle_commands(commands)

            # Sleep to avoid busy waiting
            run_loop_end_time = datetime.now(timezone.utc)
            run_loop_run_time = run_loop_end_time - run_loop_start_time
            remaining_loop_time = (
                self.RUN_LOOP_INTERVAL - run_loop_run_time
            ).total_seconds()
            if remaining_loop_time > 0:
                time.sleep(remaining_loop_time)
