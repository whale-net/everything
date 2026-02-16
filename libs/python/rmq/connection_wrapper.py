"""
Lightweight wrapper for AMQPStorm Connection to handle timeouts and retries.

This module provides a wrapper around AMQPStorm Connection that:
- Validates connection health before operations
- Automatically reconnects on stale/dead connections
- Provides retry logic for transient failures
"""

import logging
from typing import Callable, Optional, TypeVar

from amqpstorm import Connection, Channel
from amqpstorm.exception import AMQPConnectionError, AMQPChannelError

from libs.python.retry import RetryConfig, retry, is_transient_rmq_error

logger = logging.getLogger(__name__)

T = TypeVar('T')


class ResilientConnection:
    """
    Wrapper around AMQPStorm Connection that handles connection failures gracefully.
    
    This wrapper ensures operations automatically recover from connection issues by:
    - Checking connection health before operations
    - Reconnecting automatically when connection is dead
    - Retrying failed operations with exponential backoff
    """
    
    def __init__(
        self,
        connection_factory: Callable[[], Connection],
        retry_config: Optional[RetryConfig] = None,
    ):
        """
        Initialize the resilient connection wrapper.
        
        Args:
            connection_factory: Callable that returns a new Connection instance.
                               This will be called for initial connection and reconnections.
            retry_config: Optional retry configuration. If None, uses sensible defaults.
        """
        self._connection_factory = connection_factory
        self._connection: Optional[Connection] = None
        
        # Default retry config for connection operations
        if retry_config is None:
            retry_config = RetryConfig(
                max_attempts=5,
                initial_delay=1.0,
                max_delay=30.0,
                exponential_base=2.0,
                exception_filter=is_transient_rmq_error,
            )
        self._retry_config = retry_config
        
    def _ensure_connection(self) -> Connection:
        """
        Ensure we have a valid connection, reconnecting if necessary.
        
        Returns:
            Active Connection instance
            
        Raises:
            AMQPConnectionError: If unable to establish connection after retries
        """
        if self._connection is None or not self._connection.is_open:
            if self._connection is not None:
                logger.warning("Connection is closed or dead, reconnecting...")
            
            try:
                self._connection = self._connection_factory()
                logger.info("Successfully (re)connected to RabbitMQ")
            except Exception as e:
                logger.error("Failed to establish connection: %s", e)
                raise
                
        return self._connection
    
    def _execute_with_retry(self, operation: Callable[[Connection], T]) -> T:
        """
        Execute an operation with automatic retry on connection failures.
        
        This method will:
        1. Ensure connection is valid
        2. Execute the operation
        3. On failure, invalidate connection and retry (which will reconnect)
        
        Args:
            operation: Callable that takes a Connection and returns a result
            
        Returns:
            Result from the operation
            
        Raises:
            Exception from operation if all retries fail
        """
        @retry(self._retry_config)
        def _retry_wrapper():
            try:
                conn = self._ensure_connection()
                return operation(conn)
            except Exception as e:
                # On any error, invalidate the connection so retry will reconnect
                logger.warning(
                    "Operation failed with %s: %s. Invalidating connection for retry.",
                    type(e).__name__,
                    e,
                )
                self._connection = None
                raise
        
        return _retry_wrapper()
    
    def channel(self) -> Channel:
        """
        Create a new channel with automatic connection validation.
        
        Returns:
            New Channel instance
            
        Raises:
            AMQPConnectionError: If unable to create channel after retries
        """
        def _create_channel(conn: Connection) -> Channel:
            return conn.channel()
        
        return self._execute_with_retry(_create_channel)
    
    def is_open(self) -> bool:
        """
        Check if the connection is currently open.
        
        Returns:
            True if connection exists and is open, False otherwise
        """
        return self._connection is not None and self._connection.is_open
    
    def close(self) -> None:
        """
        Close the connection gracefully.
        
        This does not raise exceptions - errors are logged.
        """
        if self._connection is not None:
            try:
                if self._connection.is_open:
                    self._connection.close()
                    logger.info("Connection closed successfully")
            except Exception as e:
                logger.warning("Error closing connection: %s", e)
            finally:
                self._connection = None
    
    def __enter__(self):
        """Context manager entry."""
        self._ensure_connection()
        return self
    
    def __exit__(self, exc_type, exc_val, exc_tb):
        """Context manager exit - closes connection."""
        self.close()
        return False
