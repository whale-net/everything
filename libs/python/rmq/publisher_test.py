"""Tests for RabbitPublisher with retry logic."""

import unittest
from unittest.mock import Mock, MagicMock, patch

from amqpstorm.exception import AMQPConnectionError, AMQPChannelError

from libs.python.rmq.config import BindingConfig
from libs.python.rmq.publisher import RabbitPublisher


class TestRabbitPublisherRetry(unittest.TestCase):
    """Tests for RabbitPublisher retry functionality."""
    
    def test_publish_successful(self):
        """Test successful message publishing."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        mock_conn.channel.return_value = mock_channel
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding)
        
        # Publish message
        publisher.publish("test message")
        
        # Verify publish was called
        mock_channel.basic.publish.assert_called_once_with(
            body="test message",
            exchange="test.exchange",
            routing_key="test.key",
        )
    
    def test_publish_retries_on_channel_error(self):
        """Test that publish retries on channel errors."""
        mock_conn = Mock()
        
        # First channel fails, second succeeds
        mock_channel_fail = Mock()
        mock_channel_fail.is_open = False
        
        mock_channel_success = Mock()
        mock_channel_success.is_open = True
        
        mock_conn.channel.side_effect = [mock_channel_fail, mock_channel_success]
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding)
        
        # Should succeed after recreating channel
        publisher.publish("test message")
        
        # Verify channel was recreated
        self.assertEqual(mock_conn.channel.call_count, 2)
        mock_channel_success.basic.publish.assert_called_once()
    
    def test_publish_retries_on_connection_error(self):
        """Test that publish retries on connection errors."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        
        # First publish fails, second succeeds
        mock_channel.basic.publish.side_effect = [
            AMQPConnectionError("Connection dead"),
            None,  # Success on second attempt
        ]
        
        mock_conn.channel.return_value = mock_channel
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding)
        
        # Should succeed after retry
        publisher.publish("test message")
        
        # Verify publish was called twice
        self.assertEqual(mock_channel.basic.publish.call_count, 2)
    
    def test_publish_raises_after_max_retries(self):
        """Test that publish raises exception after max retries."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        
        # All attempts fail
        mock_channel.basic.publish.side_effect = AMQPConnectionError("Persistent error")
        
        mock_conn.channel.return_value = mock_channel
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding)
        
        # Should raise after exhausting retries
        with self.assertRaises(AMQPConnectionError):
            publisher.publish("test message")
        
        # Verify retry attempts were made
        self.assertEqual(mock_channel.basic.publish.call_count, 3)  # max_attempts in retry_config
    
    def test_publish_multiple_routing_keys(self):
        """Test publishing to multiple routing keys."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        mock_conn.channel.return_value = mock_channel
        
        binding = BindingConfig(
            exchange="test.exchange",
            routing_keys=["key1", "key2", "key3"]
        )
        publisher = RabbitPublisher(mock_conn, binding)
        
        # Publish message
        publisher.publish("test message")
        
        # Verify publish was called for each routing key
        self.assertEqual(mock_channel.basic.publish.call_count, 3)
        
        # Check each call had correct routing key
        calls = mock_channel.basic.publish.call_args_list
        self.assertEqual(calls[0].kwargs["routing_key"], "key1")
        self.assertEqual(calls[1].kwargs["routing_key"], "key2")
        self.assertEqual(calls[2].kwargs["routing_key"], "key3")
    
    def test_channel_recreation_on_closed_channel(self):
        """Test that channel is recreated when it's closed."""
        mock_conn = Mock()
        
        # Initial channel that gets closed
        mock_channel_initial = Mock()
        mock_channel_initial.is_open = False
        
        # New channel after recreation
        mock_channel_new = Mock()
        mock_channel_new.is_open = True
        
        mock_conn.channel.side_effect = [mock_channel_initial, mock_channel_new]
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding)
        
        # Publish should succeed with new channel
        publisher.publish("test message")
        
        # Verify new channel was created
        self.assertEqual(mock_conn.channel.call_count, 2)
        mock_channel_new.basic.publish.assert_called_once()


if __name__ == "__main__":
    unittest.main()
