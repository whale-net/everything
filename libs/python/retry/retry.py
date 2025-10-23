"""
Core retry functionality with exponential backoff.

Provides decorators and utilities for retrying operations with configurable
backoff strategies and exception filtering.
"""

import logging
import time
from dataclasses import dataclass, field
from functools import wraps
from typing import Callable, Optional, Tuple, Type, Union
import asyncio
import random

logger = logging.getLogger(__name__)


@dataclass
class RetryConfig:
    """Configuration for retry behavior."""
    
    max_attempts: int = 3
    """Maximum number of retry attempts (including initial attempt)"""
    
    initial_delay: float = 1.0
    """Initial delay in seconds before first retry"""
    
    max_delay: float = 60.0
    """Maximum delay in seconds between retries"""
    
    exponential_base: float = 2.0
    """Base for exponential backoff (delay *= base ** attempt)"""
    
    jitter: bool = True
    """Whether to add random jitter to delays"""
    
    jitter_factor: float = 0.1
    """Jitter factor (delay +/- delay * jitter_factor)"""
    
    exceptions: Tuple[Type[Exception], ...] = (Exception,)
    """Exception types to retry on"""
    
    exception_filter: Optional[Callable[[Exception], bool]] = None
    """Optional filter function to determine if exception should trigger retry"""
    
    on_retry: Optional[Callable[[Exception, int, float], None]] = None
    """Optional callback called before each retry: on_retry(exception, attempt, delay)"""


def _calculate_delay(config: RetryConfig, attempt: int) -> float:
    """
    Calculate delay for given attempt number.
    
    Args:
        config: Retry configuration
        attempt: Current attempt number (0-indexed)
        
    Returns:
        Delay in seconds
    """
    # Calculate exponential backoff
    delay = config.initial_delay * (config.exponential_base ** attempt)
    
    # Cap at max_delay
    delay = min(delay, config.max_delay)
    
    # Add jitter if enabled
    if config.jitter:
        jitter_amount = delay * config.jitter_factor
        delay += random.uniform(-jitter_amount, jitter_amount)
    
    # Ensure non-negative
    return max(0, delay)


def _should_retry(config: RetryConfig, exception: Exception) -> bool:
    """
    Determine if an exception should trigger a retry.
    
    Args:
        config: Retry configuration
        exception: Exception that occurred
        
    Returns:
        True if should retry, False otherwise
    """
    # Check if exception type matches
    if not isinstance(exception, config.exceptions):
        return False
    
    # Apply custom filter if provided
    if config.exception_filter:
        return config.exception_filter(exception)
    
    return True


def retry(config: Optional[RetryConfig] = None) -> Callable:
    """
    Decorator to retry a function with exponential backoff.
    
    Args:
        config: Retry configuration (uses defaults if None)
        
    Example:
        @retry(RetryConfig(max_attempts=5, initial_delay=2.0))
        def fetch_data():
            return requests.get("http://api.example.com/data")
    """
    if config is None:
        config = RetryConfig()
    
    def decorator(func: Callable) -> Callable:
        @wraps(func)
        def wrapper(*args, **kwargs):
            last_exception = None
            
            for attempt in range(config.max_attempts):
                try:
                    return func(*args, **kwargs)
                except Exception as e:
                    last_exception = e
                    
                    # Check if we should retry
                    if not _should_retry(config, e):
                        logger.debug(
                            "Exception %s does not match retry criteria, not retrying",
                            type(e).__name__,
                        )
                        raise
                    
                    # Check if we have attempts remaining
                    if attempt + 1 >= config.max_attempts:
                        logger.warning(
                            "Max retry attempts (%d) reached for %s",
                            config.max_attempts,
                            func.__name__,
                        )
                        raise
                    
                    # Calculate delay and log
                    delay = _calculate_delay(config, attempt)
                    logger.warning(
                        "Attempt %d/%d failed for %s with %s: %s. Retrying in %.2fs...",
                        attempt + 1,
                        config.max_attempts,
                        func.__name__,
                        type(e).__name__,
                        str(e),
                        delay,
                    )
                    
                    # Call retry callback if provided
                    if config.on_retry:
                        try:
                            config.on_retry(e, attempt + 1, delay)
                        except Exception as callback_error:
                            logger.error(
                                "Error in retry callback: %s",
                                callback_error,
                            )
                    
                    # Sleep before retry
                    time.sleep(delay)
            
            # Should never reach here, but just in case
            if last_exception:
                raise last_exception
            raise RuntimeError("Retry logic error: no exception to raise")
        
        return wrapper
    return decorator


def retry_async(config: Optional[RetryConfig] = None) -> Callable:
    """
    Decorator to retry an async function with exponential backoff.
    
    Args:
        config: Retry configuration (uses defaults if None)
        
    Example:
        @retry_async(RetryConfig(max_attempts=5))
        async def fetch_data():
            async with httpx.AsyncClient() as client:
                return await client.get("http://api.example.com/data")
    """
    if config is None:
        config = RetryConfig()
    
    def decorator(func: Callable) -> Callable:
        @wraps(func)
        async def wrapper(*args, **kwargs):
            last_exception = None
            
            for attempt in range(config.max_attempts):
                try:
                    return await func(*args, **kwargs)
                except Exception as e:
                    last_exception = e
                    
                    # Check if we should retry
                    if not _should_retry(config, e):
                        logger.debug(
                            "Exception %s does not match retry criteria, not retrying",
                            type(e).__name__,
                        )
                        raise
                    
                    # Check if we have attempts remaining
                    if attempt + 1 >= config.max_attempts:
                        logger.warning(
                            "Max retry attempts (%d) reached for %s",
                            config.max_attempts,
                            func.__name__,
                        )
                        raise
                    
                    # Calculate delay and log
                    delay = _calculate_delay(config, attempt)
                    logger.warning(
                        "Attempt %d/%d failed for %s with %s: %s. Retrying in %.2fs...",
                        attempt + 1,
                        config.max_attempts,
                        func.__name__,
                        type(e).__name__,
                        str(e),
                        delay,
                    )
                    
                    # Call retry callback if provided
                    if config.on_retry:
                        try:
                            config.on_retry(e, attempt + 1, delay)
                        except Exception as callback_error:
                            logger.error(
                                "Error in retry callback: %s",
                                callback_error,
                            )
                    
                    # Sleep before retry
                    await asyncio.sleep(delay)
            
            # Should never reach here, but just in case
            if last_exception:
                raise last_exception
            raise RuntimeError("Retry logic error: no exception to raise")
        
        return wrapper
    return decorator


def is_transient_http_error(exception: Exception) -> bool:
    """
    Determine if an HTTP error is transient and should be retried.
    
    Retryable errors include:
    - Connection errors (network issues, DNS failures)
    - Timeouts
    - 502 Bad Gateway
    - 503 Service Unavailable
    - 504 Gateway Timeout
    
    Args:
        exception: Exception to check
        
    Returns:
        True if error is transient and should be retried
    """
    # Handle requests library exceptions
    try:
        import requests.exceptions
        
        # Connection errors, timeouts, etc.
        if isinstance(exception, (
            requests.exceptions.ConnectionError,
            requests.exceptions.Timeout,
            requests.exceptions.ConnectTimeout,
            requests.exceptions.ReadTimeout,
        )):
            return True
        
        # HTTP errors with retryable status codes
        if isinstance(exception, requests.exceptions.HTTPError):
            response = getattr(exception, 'response', None)
            if response is not None:
                status_code = response.status_code
                # Retry on gateway errors and service unavailable
                if status_code in (502, 503, 504):
                    return True
    except ImportError:
        pass
    
    # Handle urllib3 exceptions (used by requests)
    try:
        import urllib3.exceptions
        
        if isinstance(exception, (
            urllib3.exceptions.ConnectionError,
            urllib3.exceptions.TimeoutError,
            urllib3.exceptions.ProtocolError,
            urllib3.exceptions.NewConnectionError,
            urllib3.exceptions.MaxRetryError,
        )):
            return True
    except ImportError:
        pass
    
    # Handle httpx exceptions (if using httpx)
    try:
        import httpx
        
        if isinstance(exception, (
            httpx.ConnectError,
            httpx.ConnectTimeout,
            httpx.ReadTimeout,
            httpx.WriteTimeout,
            httpx.PoolTimeout,
            httpx.NetworkError,
        )):
            return True
        
        if isinstance(exception, httpx.HTTPStatusError):
            status_code = exception.response.status_code
            if status_code in (502, 503, 504):
                return True
    except ImportError:
        pass
    
    return False


def is_transient_rmq_error(exception: Exception) -> bool:
    """
    Determine if a RabbitMQ error is transient and should be retried.
    
    Retryable errors include:
    - Connection errors
    - Channel errors that indicate connection issues
    - Authentication failures (may be temporary)
    
    Args:
        exception: Exception to check
        
    Returns:
        True if error is transient and should be retried
    """
    # Handle amqpstorm exceptions
    try:
        import amqpstorm.exception
        
        if isinstance(exception, (
            amqpstorm.exception.AMQPConnectionError,
            amqpstorm.exception.AMQPChannelError,
        )):
            return True
    except ImportError:
        pass
    
    # Handle pika exceptions (if using pika)
    try:
        import pika.exceptions
        
        if isinstance(exception, (
            pika.exceptions.AMQPConnectionError,
            pika.exceptions.ConnectionClosedByBroker,
            pika.exceptions.StreamLostError,
        )):
            return True
    except ImportError:
        pass
    
    # Generic connection errors
    if isinstance(exception, (ConnectionError, OSError)):
        return True
    
    return False
