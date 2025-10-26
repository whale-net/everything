"""Tests for ResilientConnection wrapper."""

import unittest
from unittest.mock import Mock, MagicMock, call, patch

from amqpstorm.exception import AMQPConnectionError

from libs.python.rmq.connection_wrapper import ResilientConnection
from libs.python.retry import RetryConfig


class TestResilientConnection(unittest.TestCase):
    """Tests for ResilientConnection wrapper."""
    
    def test_initial_connection_created_on_demand(self):
        """Test that connection is created only when needed."""
        mock_conn = Mock()
        mock_conn.is_open = True
        
        factory = Mock(return_value=mock_conn)
        wrapper = ResilientConnection(factory)
        
        # Connection should not be created yet
        factory.assert_not_called()
        
        # Accessing channel should trigger connection creation
        wrapper.channel()
        factory.assert_called_once()
    
    def test_channel_creation(self):
        """Test creating a channel through the wrapper."""
        mock_conn = Mock()
        mock_conn.is_open = True
        mock_channel = Mock()
        mock_conn.channel.return_value = mock_channel
        
        factory = Mock(return_value=mock_conn)
        wrapper = ResilientConnection(factory)
        
        # Create channel
        channel = wrapper.channel()
        
        # Verify
        self.assertEqual(channel, mock_channel)
        mock_conn.channel.assert_called_once()
    
    def test_reconnect_on_closed_connection(self):
        """Test that wrapper reconnects when connection is closed."""
        # First connection that gets closed
        mock_conn1 = Mock()
        mock_conn1.is_open = False
        
        # Second connection that is healthy
        mock_conn2 = Mock()
        mock_conn2.is_open = True
        mock_channel = Mock()
        mock_conn2.channel.return_value = mock_channel
        
        factory = Mock(side_effect=[mock_conn2])  # Only second connection in factory
        wrapper = ResilientConnection(factory)
        wrapper._connection = mock_conn1  # Simulate existing but closed connection
        
        # Try to create channel - should reconnect
        channel = wrapper.channel()
        
        # Verify reconnection happened (factory called to create new connection)
        self.assertEqual(factory.call_count, 1)
        self.assertEqual(channel, mock_channel)
    
    def test_retry_on_connection_error(self):
        """Test that connection errors are retried."""
        # First attempt fails
        mock_conn_fail = Mock()
        mock_conn_fail.is_open = True
        mock_conn_fail.channel.side_effect = AMQPConnectionError("Connection lost")
        
        # Second attempt succeeds
        mock_conn_success = Mock()
        mock_conn_success.is_open = True
        mock_channel = Mock()
        mock_conn_success.channel.return_value = mock_channel
        
        factory = Mock(side_effect=[mock_conn_fail, mock_conn_success])
        
        # Configure with limited retries for faster test
        retry_config = RetryConfig(
            max_attempts=3,
            initial_delay=0.01,  # Very short delay for testing
            max_delay=0.1,
            exponential_base=2.0,
        )
        
        wrapper = ResilientConnection(factory, retry_config=retry_config)
        
        # Try to create channel - should succeed after retry
        channel = wrapper.channel()
        
        # Verify retry happened
        self.assertEqual(factory.call_count, 2)
        self.assertEqual(channel, mock_channel)
    
    def test_is_open_with_no_connection(self):
        """Test is_open returns False when no connection exists."""
        factory = Mock()
        wrapper = ResilientConnection(factory)
        
        self.assertFalse(wrapper.is_open())
        factory.assert_not_called()
    
    def test_is_open_with_closed_connection(self):
        """Test is_open returns False when connection is closed."""
        mock_conn = Mock()
        mock_conn.is_open = False
        
        factory = Mock(return_value=mock_conn)
        wrapper = ResilientConnection(factory)
        wrapper._connection = mock_conn
        
        self.assertFalse(wrapper.is_open())
    
    def test_is_open_with_open_connection(self):
        """Test is_open returns True when connection is open."""
        mock_conn = Mock()
        mock_conn.is_open = True
        
        factory = Mock(return_value=mock_conn)
        wrapper = ResilientConnection(factory)
        wrapper._connection = mock_conn
        
        self.assertTrue(wrapper.is_open())
    
    def test_close_gracefully(self):
        """Test closing connection gracefully."""
        mock_conn = Mock()
        mock_conn.is_open = True
        
        factory = Mock(return_value=mock_conn)
        wrapper = ResilientConnection(factory)
        wrapper._connection = mock_conn
        
        # Close
        wrapper.close()
        
        # Verify
        mock_conn.close.assert_called_once()
        self.assertIsNone(wrapper._connection)
    
    def test_close_handles_errors(self):
        """Test that close() doesn't raise exceptions."""
        mock_conn = Mock()
        mock_conn.is_open = True
        mock_conn.close.side_effect = Exception("Close error")
        
        factory = Mock(return_value=mock_conn)
        wrapper = ResilientConnection(factory)
        wrapper._connection = mock_conn
        
        # Close should not raise
        wrapper.close()
        
        # Connection should still be cleared
        self.assertIsNone(wrapper._connection)
    
    def test_context_manager(self):
        """Test using wrapper as context manager."""
        mock_conn = Mock()
        mock_conn.is_open = True
        
        factory = Mock(return_value=mock_conn)
        
        with ResilientConnection(factory) as wrapper:
            self.assertTrue(wrapper.is_open())
        
        # Connection should be closed after context
        mock_conn.close.assert_called_once()
    
    def test_max_retries_exceeded(self):
        """Test that exception is raised when max retries exceeded."""
        mock_conn = Mock()
        mock_conn.is_open = True
        mock_conn.channel.side_effect = AMQPConnectionError("Persistent error")
        
        factory = Mock(return_value=mock_conn)
        
        # Configure with very limited retries
        retry_config = RetryConfig(
            max_attempts=2,
            initial_delay=0.01,
            max_delay=0.1,
        )
        
        wrapper = ResilientConnection(factory, retry_config=retry_config)
        
        # Should raise after exhausting retries
        with self.assertRaises(AMQPConnectionError):
            wrapper.channel()


if __name__ == "__main__":
    unittest.main()
