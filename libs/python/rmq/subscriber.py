"""
RabbitMQ subscriber implementations.

This module contains concrete implementations of message subscribers
for receiving messages via RabbitMQ.
"""

import logging
import queue
import threading
from typing import List, Union

from amqpstorm import Channel, Connection, Message

from libs.python.rmq.config import BindingConfig, QueueConfig
from libs.python.rmq.interface import MessageSubscriberInterface

logger = logging.getLogger(__name__)


class RabbitSubscriber(MessageSubscriberInterface):
    """
    Base class for RabbitMQ subscribers.
    This class provides common functionality for subscribing to RabbitMQ exchanges.
    """

    def __init__(
        self,
        connection: Connection,
        binding_configs: Union[BindingConfig, list[BindingConfig]],
        queue_config: QueueConfig,
    ) -> None:
        """
        Initialize the RabbitMQ subscriber.
        
        Args:
            connection: Active RabbitMQ connection
            binding_configs: Single or list of binding configurations
            queue_config: Queue configuration
        """
        self._channel: Channel = connection.channel()
        # Set QoS to ensure fair dispatching of messages
        self._channel.basic.qos(prefetch_count=1)

        if isinstance(binding_configs, BindingConfig):
            binding_configs = [binding_configs]
        self._binding_configs: list[BindingConfig] = binding_configs

        # Declare queue
        logger.info("Declaring queue with config: %s", queue_config)
        result = self._channel.queue.declare(
            queue=queue_config.name or "",
            durable=queue_config.durable,
            exclusive=queue_config.exclusive,
            auto_delete=queue_config.auto_delete,
        )

        # Store actual queue name (important for server-generated names)
        queue_config.actual_queue_name = result.get("queue", queue_config.name)
        logger.info("Queue declared: %s", queue_config.actual_queue_name)

        # Bind queue to exchanges with routing keys
        for binding_config in self._binding_configs:
            for routing_key in binding_config.routing_keys:
                self._channel.queue.bind(
                    exchange=binding_config.exchange,
                    queue=queue_config.actual_queue_name,
                    routing_key=str(routing_key),
                )
                logger.info(
                    "Queue %s bound to exchange %s with routing key '%s'",
                    queue_config.actual_queue_name,
                    binding_config.exchange,
                    routing_key,
                )

        # Internal queue for message buffering
        self._internal_message_queue = queue.Queue()
        
        # Start consuming messages
        self._consumer_tag = self._channel.basic.consume(
            callback=self._message_handler,
            queue=queue_config.actual_queue_name,
        )

        self._consumer_thread = threading.Thread(
            target=self._channel.start_consuming,
            name=f"rmq-subscriber-{queue_config.actual_queue_name}",
            daemon=True,
        )
        self._consumer_thread.start()

        logger.info("RabbitSubscriber initialized with channel %s", self._channel)

    def _message_handler(self, message: Message) -> None:
        """
        Internal handler for incoming messages.
        
        Writes messages to internal queue for retrieval in `consume` method
        and acknowledges them.
        
        Args:
            message: Incoming AMQP message
        """
        self._internal_message_queue.put(message.body)
        message.ack()
        logger.info("Message received and acknowledged: %s", message.delivery_tag)

    def consume(self) -> List[str]:
        """
        Consume messages from the internal queue.
        
        This method retrieves all available messages and is non-blocking.

        Returns:
            List of message bodies as strings
        """
        messages = []
        while not self._internal_message_queue.empty():
            try:
                # Non-blocking get - returns immediately if no messages available
                message_body = self._internal_message_queue.get(block=False)
                messages.append(message_body)
            except queue.Empty:
                break
        return messages

    def shutdown(self) -> None:
        """
        Shutdown the subscriber by stopping consumption and closing the channel.
        """
        logger.info("Shutting down RabbitSubscriber...")

        try:
            # Cancel the consumer
            if self._consumer_tag and self._channel.is_open:
                self._channel.basic.cancel(self._consumer_tag)
                logger.info("Consumer cancelled.")
        except Exception as e:
            logger.exception("Error cancelling consumer: %s", e)

        try:
            # Stop consuming
            if self._channel.is_open:
                self._channel.stop_consuming()
                logger.info("Stopped consuming.")
        except Exception as e:
            logger.exception("Error stopping consuming: %s", e)

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
