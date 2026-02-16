"""
RabbitMQ publisher implementations.

This module contains concrete implementations of message publishers
for sending messages via RabbitMQ.
"""

import logging
from typing import Optional, Union

from amqpstorm import Channel, Connection

from libs.python.rmq.config import BindingConfig
from libs.python.rmq.interface import MessagePublisherInterface
from libs.python.retry import RetryConfig, retry, is_transient_rmq_error

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
        retry_config: Optional[RetryConfig] = None,
    ) -> None:
        """
        Initialize the RabbitMQ publisher.
        
        Args:
            connection: Active RabbitMQ connection
            binding_configs: Single or list of binding configurations
            retry_config: Optional retry configuration for publish operations.
                         If None (default), no retry is performed.
        """
        self._connection = connection
        self._channel: Channel = connection.channel()

        if isinstance(binding_configs, BindingConfig):
            binding_configs = [binding_configs]
        self._binding_configs: list[BindingConfig] = binding_configs
        
        # Store retry config (None means no retry)
        self._retry_config = retry_config

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
        
        If retry_config was provided during initialization, publish operations will
        automatically retry on transient failures like connection timeouts.

        Args:
            message: The message to be published
        """
        def _do_publish():
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
        
        # Apply retry if configured
        if self._retry_config:
            retry_decorator = retry(self._retry_config)
            retry_decorator(_do_publish)()
        else:
            _do_publish()

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
