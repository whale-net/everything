"""Tests for retry functionality."""

import asyncio
import pytest
from unittest.mock import Mock, call

from libs.python.retry import (
    RetryConfig,
    retry,
    retry_async,
    is_transient_http_error,
    is_transient_rmq_error,
)


class TestRetryConfig:
    """Test RetryConfig dataclass."""
    
    def test_default_config(self):
        config = RetryConfig()
        assert config.max_attempts == 3
        assert config.initial_delay == 1.0
        assert config.max_delay == 60.0
        assert config.exponential_base == 2.0
        assert config.jitter is True
        assert config.jitter_factor == 0.1
    
    def test_custom_config(self):
        config = RetryConfig(
            max_attempts=5,
            initial_delay=2.0,
            max_delay=30.0,
        )
        assert config.max_attempts == 5
        assert config.initial_delay == 2.0
        assert config.max_delay == 30.0


class TestRetryDecorator:
    """Test retry decorator."""
    
    def test_success_no_retry(self):
        """Function succeeds on first attempt."""
        mock_func = Mock(return_value="success")
        
        @retry(RetryConfig(max_attempts=3))
        def test_func():
            return mock_func()
        
        result = test_func()
        assert result == "success"
        assert mock_func.call_count == 1
    
    def test_retry_on_exception(self):
        """Function fails then succeeds."""
        mock_func = Mock(side_effect=[ValueError("fail"), "success"])
        
        @retry(RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            jitter=False,
        ))
        def test_func():
            return mock_func()
        
        result = test_func()
        assert result == "success"
        assert mock_func.call_count == 2
    
    def test_max_attempts_exceeded(self):
        """Function fails all attempts."""
        mock_func = Mock(side_effect=ValueError("fail"))
        
        @retry(RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            jitter=False,
        ))
        def test_func():
            return mock_func()
        
        with pytest.raises(ValueError, match="fail"):
            test_func()
        
        assert mock_func.call_count == 3
    
    def test_exception_filter(self):
        """Only retry filtered exceptions."""
        def should_retry(exc):
            return "retry" in str(exc).lower()
        
        mock_func = Mock(side_effect=ValueError("do not retry"))
        
        @retry(RetryConfig(
            max_attempts=3,
            exception_filter=should_retry,
        ))
        def test_func():
            return mock_func()
        
        # Should fail immediately without retry
        with pytest.raises(ValueError):
            test_func()
        
        assert mock_func.call_count == 1
    
    def test_on_retry_callback(self):
        """Callback is called on retry."""
        callback_mock = Mock()
        mock_func = Mock(side_effect=[ValueError("fail"), "success"])
        
        @retry(RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            jitter=False,
            on_retry=callback_mock,
        ))
        def test_func():
            return mock_func()
        
        result = test_func()
        assert result == "success"
        
        # Callback should be called once (before second attempt)
        assert callback_mock.call_count == 1
        callback_mock.assert_called_once()
        
        # Check callback arguments
        args = callback_mock.call_args[0]
        assert isinstance(args[0], ValueError)  # exception
        assert args[1] == 1  # attempt number
        assert isinstance(args[2], float)  # delay


class TestRetryAsyncDecorator:
    """Test async retry decorator."""
    
    @pytest.mark.asyncio
    async def test_success_no_retry(self):
        """Async function succeeds on first attempt."""
        mock_func = Mock(return_value="success")
        
        @retry_async(RetryConfig(max_attempts=3))
        async def test_func():
            return mock_func()
        
        result = await test_func()
        assert result == "success"
        assert mock_func.call_count == 1
    
    @pytest.mark.asyncio
    async def test_retry_on_exception(self):
        """Async function fails then succeeds."""
        mock_func = Mock(side_effect=[ValueError("fail"), "success"])
        
        @retry_async(RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            jitter=False,
        ))
        async def test_func():
            return mock_func()
        
        result = await test_func()
        assert result == "success"
        assert mock_func.call_count == 2
    
    @pytest.mark.asyncio
    async def test_max_attempts_exceeded(self):
        """Async function fails all attempts."""
        mock_func = Mock(side_effect=ValueError("fail"))
        
        @retry_async(RetryConfig(
            max_attempts=3,
            initial_delay=0.01,
            jitter=False,
        ))
        async def test_func():
            return mock_func()
        
        with pytest.raises(ValueError, match="fail"):
            await test_func()
        
        assert mock_func.call_count == 3


class TestHTTPErrorDetection:
    """Test HTTP error detection."""
    
    def test_detects_connection_error(self):
        """Detect connection errors as transient."""
        try:
            import requests.exceptions
            exc = requests.exceptions.ConnectionError()
            assert is_transient_http_error(exc) is True
        except ImportError:
            pytest.skip("requests not available")
    
    def test_detects_timeout(self):
        """Detect timeouts as transient."""
        try:
            import requests.exceptions
            exc = requests.exceptions.Timeout()
            assert is_transient_http_error(exc) is True
        except ImportError:
            pytest.skip("requests not available")
    
    def test_detects_502_error(self):
        """Detect 502 as transient."""
        try:
            import requests
            import requests.exceptions
            
            response = Mock()
            response.status_code = 502
            exc = requests.exceptions.HTTPError(response=response)
            
            assert is_transient_http_error(exc) is True
        except ImportError:
            pytest.skip("requests not available")
    
    def test_does_not_detect_404(self):
        """404 is not transient."""
        try:
            import requests
            import requests.exceptions
            
            response = Mock()
            response.status_code = 404
            exc = requests.exceptions.HTTPError(response=response)
            
            assert is_transient_http_error(exc) is False
        except ImportError:
            pytest.skip("requests not available")
    
    def test_does_not_detect_value_error(self):
        """Non-HTTP errors are not transient."""
        exc = ValueError("not an HTTP error")
        assert is_transient_http_error(exc) is False


class TestRMQErrorDetection:
    """Test RabbitMQ error detection."""
    
    def test_detects_connection_error(self):
        """Detect RMQ connection errors as transient."""
        try:
            import amqpstorm.exception
            exc = amqpstorm.exception.AMQPConnectionError("connection failed")
            assert is_transient_rmq_error(exc) is True
        except ImportError:
            pytest.skip("amqpstorm not available")
    
    def test_detects_generic_connection_error(self):
        """Detect generic connection errors as transient."""
        exc = ConnectionError("connection failed")
        assert is_transient_rmq_error(exc) is True
    
    def test_does_not_detect_value_error(self):
        """Non-RMQ errors are not transient."""
        exc = ValueError("not an RMQ error")
        assert is_transient_rmq_error(exc) is False


class TestExponentialBackoff:
    """Test exponential backoff calculation."""
    
    def test_backoff_increases(self):
        """Delays increase exponentially."""
        mock_func = Mock(side_effect=ValueError("fail"))
        delays = []
        
        def capture_delay(exc, attempt, delay):
            delays.append(delay)
        
        @retry(RetryConfig(
            max_attempts=5,
            initial_delay=1.0,
            exponential_base=2.0,
            jitter=False,
            on_retry=capture_delay,
        ))
        def test_func():
            return mock_func()
        
        with pytest.raises(ValueError):
            test_func()
        
        # Check delays increase exponentially
        assert len(delays) == 4  # 4 retries after initial attempt
        assert delays[0] == 1.0  # 1.0 * 2^0
        assert delays[1] == 2.0  # 1.0 * 2^1
        assert delays[2] == 4.0  # 1.0 * 2^2
        assert delays[3] == 8.0  # 1.0 * 2^3
    
    def test_max_delay_cap(self):
        """Delays are capped at max_delay."""
        mock_func = Mock(side_effect=ValueError("fail"))
        delays = []
        
        def capture_delay(exc, attempt, delay):
            delays.append(delay)
        
        @retry(RetryConfig(
            max_attempts=10,
            initial_delay=1.0,
            max_delay=5.0,
            exponential_base=2.0,
            jitter=False,
            on_retry=capture_delay,
        ))
        def test_func():
            return mock_func()
        
        with pytest.raises(ValueError):
            test_func()
        
        # All delays should be <= max_delay
        assert all(d <= 5.0 for d in delays)
        # Later delays should hit the cap
        assert delays[-1] == 5.0
