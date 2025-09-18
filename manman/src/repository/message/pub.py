import logging

from manman.models import Command, ExternalStatusInfo, InternalStatusInfo
from manman.repository.message.abstract_interface import MessagePublisherInterface

logger = logging.getLogger(__name__)


class PubServiceInterface:
    def __init__(self, publisher: MessagePublisherInterface) -> None:
        self._publisher = publisher
        logger.info(
            "%s initialized with publisher %s",
            self.__class__.__name__,
            self._publisher,
        )


class InternalStatusInfoPubService(PubServiceInterface):
    def publish_status(self, internal_status: InternalStatusInfo) -> None:
        """
        Publish a status message to RabbitMQ.

        :param status_info: The status information to publish.
        """
        message = internal_status.model_dump_json()
        self._publisher.publish(message)


class ExternalStatusInfoPubService(PubServiceInterface):
    def publish_external_status(self, external_status: ExternalStatusInfo) -> None:
        """
        Publish an external status message to RabbitMQ.

        :param external_status: The external status information to publish.
        """
        message = external_status.model_dump_json()
        self._publisher.publish(message)


class CommandPubService(PubServiceInterface):
    def publish_command(self, command: Command) -> None:
        """
        Publish a command message to RabbitMQ.

        :param command: The command to publish.
        """
        message = command.model_dump_json()
        self._publisher.publish(message)
