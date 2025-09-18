import logging

from manman.models import Command, ExternalStatusInfo, InternalStatusInfo
from manman.repository.message.abstract_interface import MessageSubscriberInterface

logger = logging.getLogger(__name__)


class SubServiceInterface:
    def __init__(self, subscriber: MessageSubscriberInterface) -> None:
        self._subscriber = subscriber
        logger.info(
            "%s initialized with subscriber %s",
            self.__class__.__name__,
            self._subscriber,
        )


class CommandSubService(SubServiceInterface):
    """
    Service for subscribing to messages
    """

    def get_commands(self) -> list[Command]:
        """
        Consume a command message from RabbitMQ.

        :return: The consumed command.
        """

        commands = []
        for message in self._subscriber.consume():
            command = Command.model_validate_json(message)
            commands.append(command)
        return commands


class InternalStatusSubService(SubServiceInterface):
    """
    Service for subscribing to internal status messages
    """

    def get_internal_statuses(self) -> list[InternalStatusInfo]:
        """
        Consume an internal status message from RabbitMQ.

        :return: The consumed internal status.
        """

        statuses = []
        for message in self._subscriber.consume():
            status = InternalStatusInfo.model_validate_json(message)
            statuses.append(status)
        return statuses


class ExternalStatusSubService(SubServiceInterface):
    """
    Service for subscribing to external status messages
    """

    def get_external_status_infos(self) -> list[ExternalStatusInfo]:
        """
        Consume an external status message from RabbitMQ.

        :return: The consumed external status.
        """

        statuses = []
        for message in self._subscriber.consume():
            status = ExternalStatusInfo.model_validate_json(message)
            statuses.append(status)
        return statuses
