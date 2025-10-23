"""Tests for the hello-logging demo app."""

import logging
from unittest.mock import patch, MagicMock
from libs.python.logging import configure_logging, get_logger
from demo.hello_logging.main import (
    simulate_request_handler,
    simulate_error_handling,
    demonstrate_worker_logging,
)


def test_logging_configuration():
    """Test that logging configuration works."""
    context = configure_logging(
        app_name="test-app",
        domain="test",
        app_type="worker",
        environment="test",
        force_reconfigure=True,
        enable_otlp=False,
        json_format=False,
    )
    
    assert context.app_name == "test-app"
    assert context.domain == "test"
    assert context.environment == "test"


def test_logger_creation():
    """Test that logger creation works."""
    logger = get_logger(__name__)
    assert logger is not None
    
    # Logger should work without errors
    logger.info("Test message")
    logger.debug("Debug message")


def test_request_handler_logging():
    """Test request handler simulation."""
    # Should not raise any exceptions
    simulate_request_handler()


def test_error_handling_logging():
    """Test error handling simulation."""
    # Should log exception without raising
    simulate_error_handling()


def test_worker_logging():
    """Test worker logging simulation."""
    # Should not raise any exceptions
    demonstrate_worker_logging()
