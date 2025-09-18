"""
RabbitMQ publisher implementations.

This module contains concrete implementations of message publishers
for sending status messages via RabbitMQ.
"""

import logging
from typing import Union

from amqpstorm import Channel, Connection

from manman.repository.message.abstract_interface import MessagePublisherInterface
from manman.repository.rabbitmq.config import BindingConfig

logger = logging.getLogger(__name__)


class RabbitPublisher(MessagePublisherInterface):
    """
    Base class for RabbitMQ publishers.
    This class provides common functionality for publishing messages to RabbitMQ exchanges.
    """

    def __init__(
        self,
        connection: Connection,
        binding_configs: Union[BindingConfig, list[BindingConfig]],
    ) -> None:
        self._channel: Channel = connection.channel()

        if isinstance(binding_configs, BindingConfig):
            binding_configs = [binding_configs]
        self._binding_configs: list[BindingConfig] = binding_configs

        logger.info("RabbitPublisher initialized with channel %s", self._channel)

    def publish(self, message: str) -> None:
        """
        Publish a message to all configured exchanges with their routing keys.

        :param message: The message to be published.
        """
        for binding_config in self._binding_configs:
            for routing_key in binding_config.routing_keys:
                self._channel.basic.publish(
                    body=message,
                    exchange=binding_config.exchange,
                    routing_key=str(routing_key),
                )
                logger.debug(
                    "Message published to exchange %s with routing key %s",
                    binding_config.exchange,
                    routing_key,
                )

    def __del__(self) -> None:
        """
        Destructor to ensure the channel is closed when the object is deleted.
        """
        try:
            self.shutdown()
        except Exception:
            # Suppress exceptions during cleanup to avoid issues during interpreter shutdown
            pass

    # TODO - move this to common base rabbit connection wrapper class or something
    def shutdown(self) -> None:
        """
        Shutdown the publisher by closing the channel.
        """
        logger.info("Shutting down RabbitPublisher...")
        try:
            if self._channel.is_open:
                self._channel.close()
                logger.info("Channel closed.")
        except Exception as e:
            logger.exception("Error closing channel: %s", e)
