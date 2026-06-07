"""Tests for RabbitPublisher with retry logic."""

import pytest
from unittest.mock import Mock

from amqpstorm.exception import AMQPConnectionError

from libs.python.rmq.config import BindingConfig
from libs.python.rmq.publisher import RabbitPublisher
from libs.python.retry import RetryConfig


class TestRabbitPublisherRetry:
    """Tests for RabbitPublisher retry functionality."""
    
    def test_publish_successful_no_retry(self):
        """Test successful message publishing without retry."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        mock_conn.channel.return_value = mock_channel
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding)  # No retry_config
        
        # Publish message
        publisher.publish("test message")
        
        # Verify publish was called
        mock_channel.basic.publish.assert_called_once_with(
            body="test message",
            exchange="test.exchange",
            routing_key="test.key",
        )
    
    def test_publish_successful_with_retry_config(self):
        """Test successful message publishing with retry config provided."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        mock_conn.channel.return_value = mock_channel
        
        retry_config = RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            max_delay=0.1,
        )
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding, retry_config=retry_config)
        
        # Publish message
        publisher.publish("test message")
        
        # Verify publish was called
        mock_channel.basic.publish.assert_called_once_with(
            body="test message",
            exchange="test.exchange",
            routing_key="test.key",
        )
    
    def test_publish_retries_on_channel_error(self):
        """Test that publish retries on channel errors when retry_config is provided."""
        mock_conn = Mock()
        
        # First channel fails, second succeeds
        mock_channel_fail = Mock()
        mock_channel_fail.is_open = False
        
        mock_channel_success = Mock()
        mock_channel_success.is_open = True
        
        mock_conn.channel.side_effect = [mock_channel_fail, mock_channel_success]
        
        retry_config = RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            max_delay=0.1,
        )
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding, retry_config=retry_config)
        
        # Should succeed after recreating channel
        publisher.publish("test message")
        
        # Verify channel was recreated
        assert mock_conn.channel.call_count == 2
        mock_channel_success.basic.publish.assert_called_once()
    
    def test_publish_retries_on_connection_error(self):
        """Test that publish retries on connection errors when retry_config is provided."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        
        # First publish fails, second succeeds
        mock_channel.basic.publish.side_effect = [
            AMQPConnectionError("Connection dead"),
            None,  # Success on second attempt
        ]
        
        mock_conn.channel.return_value = mock_channel
        
        retry_config = RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            max_delay=0.1,
        )
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding, retry_config=retry_config)
        
        # Should succeed after retry
        publisher.publish("test message")
        
        # Verify publish was called twice
        assert mock_channel.basic.publish.call_count == 2
    
    def test_publish_raises_after_max_retries(self):
        """Test that publish raises exception after max retries when retry_config is provided."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        
        # All attempts fail
        mock_channel.basic.publish.side_effect = AMQPConnectionError("Persistent error")
        
        mock_conn.channel.return_value = mock_channel
        
        retry_config = RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            max_delay=0.1,
        )
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding, retry_config=retry_config)
        
        # Should raise after exhausting retries
        with pytest.raises(AMQPConnectionError):
            publisher.publish("test message")
        
        # Verify retry attempts were made
        assert mock_channel.basic.publish.call_count == 3  # max_attempts
    
    def test_publish_no_retry_on_error_without_config(self):
        """Test that publish does not retry on error when no retry_config is provided."""
        mock_conn = Mock()
        mock_channel = Mock()
        mock_channel.is_open = True
        
        # First attempt fails
        mock_channel.basic.publish.side_effect = AMQPConnectionError("Connection dead")
        
        mock_conn.channel.return_value = mock_channel
        
        binding = BindingConfig(exchange="test.exchange", routing_keys=["test.key"])
        publisher = RabbitPublisher(mock_conn, binding)  # No retry_config
        
        # Should raise immediately without retry
        with pytest.raises(AMQPConnectionError):
            publisher.publish("test message")
        
        # Verify only one attempt was made
        assert mock_channel.basic.publish.call_count == 1
    
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
        assert mock_channel.basic.publish.call_count == 3
        
        # Check each call had correct routing key
        calls = mock_channel.basic.publish.call_args_list
        assert calls[0].kwargs["routing_key"] == "key1"
        assert calls[1].kwargs["routing_key"] == "key2"
        assert calls[2].kwargs["routing_key"] == "key3"
    
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
        assert mock_conn.channel.call_count == 2
        mock_channel_new.basic.publish.assert_called_once()


if __name__ == "__main__":
    pytest.main([__file__])
