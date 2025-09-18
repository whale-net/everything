"""
Test RabbitSubscriber multiple exchanges functionality.

This module tests the RabbitSubscriber that supports
consuming from multiple exchanges with different routing keys.
"""

import unittest
from unittest.mock import Mock, call, patch

from manman.repository.rabbitmq.config import (
    BindingConfig,
    EntityRegistry,
    ExchangeRegistry,
    MessageTypeRegistry,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
)
from manman.repository.rabbitmq.subscriber import RabbitSubscriber


class TestRabbitSubscriberMultipleExchanges(unittest.TestCase):
    """Test cases for RabbitSubscriber with multiple exchanges."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_connection = Mock()
        self.mock_channel = Mock()
        self.mock_connection.channel.return_value = self.mock_channel

        # Mock queue declaration response
        self.mock_channel.queue.declare.return_value = {"queue": "test-queue-123"}
        self.mock_channel.basic.consume.return_value = "consumer-tag-123"

    @patch("manman.repository.rabbitmq.subscriber.threading.Thread")
    def test_single_binding_config(self, mock_thread):
        """Test RabbitSubscriber with single binding configuration."""
        # Arrange
        routing_key = RoutingKeyConfig(
            entity=EntityRegistry.WORKER,
            identifier=TopicWildcard.ANY,
            type=MessageTypeRegistry.STATUS,
        )
        binding_config = BindingConfig(
            exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
            routing_keys=[routing_key],
        )
        queue_config = QueueConfig(
            name="test-queue",
            durable=True,
            exclusive=False,
            auto_delete=False,
        )

        # Act
        subscriber = RabbitSubscriber(
            connection=self.mock_connection,
            binding_configs=binding_config,
            queue_config=queue_config,
        )

        # Assert
        self.assertEqual(len(subscriber._binding_configs), 1)
        self.assertEqual(subscriber._binding_configs[0], binding_config)

        # Verify queue binding was called correctly
        self.mock_channel.queue.bind.assert_called_once_with(
            exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
            queue="test-queue-123",
            routing_key=str(routing_key),
        )

    @patch("manman.repository.rabbitmq.subscriber.threading.Thread")
    def test_multiple_binding_configs(self, mock_thread):
        """Test RabbitSubscriber with multiple binding configurations."""
        # Arrange
        worker_routing_key = RoutingKeyConfig(
            entity=EntityRegistry.WORKER,
            identifier=TopicWildcard.ANY,
            type=MessageTypeRegistry.STATUS,
        )
        gsi_routing_key = RoutingKeyConfig(
            entity=EntityRegistry.GAME_SERVER_INSTANCE,
            identifier=TopicWildcard.ANY,
            type=MessageTypeRegistry.STATUS,
        )

        binding_configs = [
            BindingConfig(
                exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
                routing_keys=[worker_routing_key],
            ),
            BindingConfig(
                exchange=ExchangeRegistry.EXTERNAL_SERVICE_EVENT,
                routing_keys=[gsi_routing_key],
            ),
        ]

        queue_config = QueueConfig(
            name="test-queue",
            durable=True,
            exclusive=False,
            auto_delete=False,
        )

        # Act
        subscriber = RabbitSubscriber(
            connection=self.mock_connection,
            binding_configs=binding_configs,
            queue_config=queue_config,
        )

        # Assert
        self.assertEqual(len(subscriber._binding_configs), 2)
        self.assertEqual(subscriber._binding_configs, binding_configs)

        # Verify queue binding was called for each binding config
        expected_calls = [
            call(
                exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
                queue="test-queue-123",
                routing_key=str(worker_routing_key),
            ),
            call(
                exchange=ExchangeRegistry.EXTERNAL_SERVICE_EVENT,
                queue="test-queue-123",
                routing_key=str(gsi_routing_key),
            ),
        ]
        self.mock_channel.queue.bind.assert_has_calls(expected_calls, any_order=True)

    @patch("manman.repository.rabbitmq.subscriber.threading.Thread")
    def test_multiple_routing_keys_per_exchange(self, mock_thread):
        """Test RabbitSubscriber with multiple routing keys per exchange."""
        # Arrange
        worker_routing_key = RoutingKeyConfig(
            entity=EntityRegistry.WORKER,
            identifier=TopicWildcard.ANY,
            type=MessageTypeRegistry.STATUS,
        )
        gsi_routing_key = RoutingKeyConfig(
            entity=EntityRegistry.GAME_SERVER_INSTANCE,
            identifier=TopicWildcard.ANY,
            type=MessageTypeRegistry.STATUS,
        )

        binding_config = BindingConfig(
            exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
            routing_keys=[worker_routing_key, gsi_routing_key],
        )

        queue_config = QueueConfig(
            name="test-queue",
            durable=True,
            exclusive=False,
            auto_delete=False,
        )

        # Act
        subscriber = RabbitSubscriber(
            connection=self.mock_connection,
            binding_configs=binding_config,
            queue_config=queue_config,
        )

        # Assert
        self.assertEqual(len(subscriber._binding_configs), 1)
        self.assertEqual(len(subscriber._binding_configs[0].routing_keys), 2)

        # Verify queue binding was called for each routing key
        expected_calls = [
            call(
                exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
                queue="test-queue-123",
                routing_key=str(worker_routing_key),
            ),
            call(
                exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
                queue="test-queue-123",
                routing_key=str(gsi_routing_key),
            ),
        ]
        self.mock_channel.queue.bind.assert_has_calls(expected_calls, any_order=True)

    @patch("manman.repository.rabbitmq.subscriber.threading.Thread")
    def test_message_consumption(self, mock_thread):
        """Test that messages can be consumed correctly."""
        # Arrange
        routing_key = RoutingKeyConfig(
            entity=EntityRegistry.WORKER,
            identifier=TopicWildcard.ANY,
            type=MessageTypeRegistry.STATUS,
        )
        binding_config = BindingConfig(
            exchange=ExchangeRegistry.INTERNAL_SERVICE_EVENT,
            routing_keys=[routing_key],
        )
        queue_config = QueueConfig(
            name="test-queue",
            durable=True,
            exclusive=False,
            auto_delete=False,
        )

        subscriber = RabbitSubscriber(
            connection=self.mock_connection,
            binding_configs=binding_config,
            queue_config=queue_config,
        )

        # Create test message
        test_message_body = '{"test": "message"}'

        # Simulate message in internal queue
        subscriber._internal_message_queue.put(test_message_body)

        # Act
        messages = subscriber.consume()

        # Assert
        self.assertEqual(len(messages), 1)
        self.assertEqual(messages[0], test_message_body)


if __name__ == "__main__":
    unittest.main()
