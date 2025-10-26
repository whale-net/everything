"""
RabbitMQ publisher implementations.

This module contains concrete implementations of message publishers
for sending messages via RabbitMQ.
"""

import logging
from typing import Union

from amqpstorm import Channel, Connection

from libs.python.rmq.config import BindingConfig
from libs.python.rmq.connection_wrapper import ResilientConnection
from libs.python.rmq.interface import MessagePublisherInterface
from libs.python.retry import RetryConfig, retry, is_transient_rmq_error

logger = logging.getLogger(__name__)


class RabbitPublisher(MessagePublisherInterface):
    """
    Base class for RabbitMQ publishers.
    This class provides common functionality for publishing messages to RabbitMQ exchanges.
    
    Includes automatic retry logic for publish operations to handle transient connection failures.
    """

    def __init__(
        self,
        connection: Connection,
        binding_configs: Union[BindingConfig, list[BindingConfig]],
    ) -> None:
        """
        Initialize the RabbitMQ publisher.
        
        Args:
            connection: Active RabbitMQ connection
            binding_configs: Single or list of binding configurations
        """
        self._connection = connection
        self._channel: Channel = connection.channel()

        if isinstance(binding_configs, BindingConfig):
            binding_configs = [binding_configs]
        self._binding_configs: list[BindingConfig] = binding_configs
        
        # Configure retry for publish operations
        self._retry_config = RetryConfig(
            max_attempts=3,
            initial_delay=0.5,
            max_delay=5.0,
            exponential_base=2.0,
            exception_filter=is_transient_rmq_error,
        )

        logger.info("RabbitPublisher initialized with channel %s", self._channel)

    def _ensure_channel(self) -> Channel:
        """
        Ensure channel is open, recreating if necessary.
        
        Returns:
            Open channel
        """
        if not self._channel or not self._channel.is_open:
            logger.warning("Channel is closed, recreating from connection")
            self._channel = self._connection.channel()
        return self._channel
    
    def publish(self, message: str) -> None:
        """
        Publish a message to all configured exchanges with their routing keys.
        
        Includes automatic retry logic for transient failures like connection timeouts.

        Args:
            message: The message to be published
        """
        @retry(self._retry_config)
        def _publish_with_retry():
            channel = self._ensure_channel()
            for binding_config in self._binding_configs:
                for routing_key in binding_config.routing_keys:
                    channel.basic.publish(
                        body=message,
                        exchange=binding_config.exchange,
                        routing_key=str(routing_key),
                    )
                    logger.debug(
                        "Message published to exchange %s with routing key %s",
                        binding_config.exchange,
                        routing_key,
                    )
        
        _publish_with_retry()

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

    def __del__(self) -> None:
        """
        Destructor to ensure the channel is closed when the object is deleted.
        """
        try:
            self.shutdown()
        except Exception:
            # Suppress exceptions during cleanup to avoid issues during interpreter shutdown
            pass
