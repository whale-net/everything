"""Tests for unified logging configuration."""

import logging
import os
import sys
from io import StringIO

import pytest

# Import the log_setup module
from libs.python import log_setup as unified_logging


def test_setup_logging_basic():
    """Test basic logging setup without OTEL."""
    # Reset logging
    root = logging.getLogger()
    for handler in root.handlers[:]:
        root.removeHandler(handler)

    # Setup with basic parameters
    unified_logging.setup_logging(
        level=logging.INFO,
        app_name="test-app",
        app_type="external-api",
        domain="test",
        enable_otel=False,
        enable_console=True,
        force_setup=True,
    )

    # Verify logger is configured
    assert root.level == logging.INFO
    assert len(root.handlers) > 0


def test_setup_logging_with_app_env():
    """Test logging setup with APP_ENV environment variable."""
    # Reset logging
    root = logging.getLogger()
    for handler in root.handlers[:]:
        root.removeHandler(handler)

    # Set APP_ENV
    os.environ["APP_ENV"] = "test-env"

    # Setup without explicit app_env
    unified_logging.setup_logging(
        level=logging.INFO,
        app_name="test-app",
        domain="test",
        enable_otel=False,
        enable_console=True,
        force_setup=True,
    )

    # Verify logger is configured
    assert root.level == logging.INFO

    # Cleanup
    del os.environ["APP_ENV"]


def test_create_formatter_with_metadata():
    """Test formatter creation with app metadata."""
    formatter = unified_logging.create_formatter(
        app_name="test-app",
        app_type="external-api",
        domain="test",
        app_env="dev",
    )

    # Create a log record
    record = logging.LogRecord(
        name="test.logger",
        level=logging.INFO,
        pathname="test.py",
        lineno=1,
        msg="Test message",
        args=(),
        exc_info=None,
    )

    # Format the record
    formatted = formatter.format(record)

    # Verify format includes metadata
    assert "test" in formatted
    assert "test-app" in formatted
    assert "external-api" in formatted
    assert "dev" in formatted
    assert "Test message" in formatted


def test_create_formatter_partial_metadata():
    """Test formatter creation with partial metadata."""
    formatter = unified_logging.create_formatter(
        app_name="test-app",
        domain="test",
    )

    # Create a log record
    record = logging.LogRecord(
        name="test.logger",
        level=logging.INFO,
        pathname="test.py",
        lineno=1,
        msg="Test message",
        args=(),
        exc_info=None,
    )

    # Format the record
    formatted = formatter.format(record)

    # Verify format includes available metadata
    assert "test" in formatted
    assert "test-app" in formatted
    assert "Test message" in formatted


def test_create_formatter_no_metadata():
    """Test formatter creation without metadata."""
    formatter = unified_logging.create_formatter()

    # Create a log record
    record = logging.LogRecord(
        name="test.logger",
        level=logging.INFO,
        pathname="test.py",
        lineno=1,
        msg="Test message",
        args=(),
        exc_info=None,
    )

    # Format the record
    formatted = formatter.format(record)

    # Verify format works without metadata
    assert "Test message" in formatted


def test_setup_logging_idempotent():
    """Test that setup_logging is idempotent by default."""
    # Reset logging
    root = logging.getLogger()
    for handler in root.handlers[:]:
        root.removeHandler(handler)

    # First setup
    unified_logging.setup_logging(
        level=logging.INFO,
        app_name="test-app",
        enable_otel=False,
        enable_console=True,
        force_setup=True,
    )

    handler_count = len(root.handlers)

    # Second setup without force_setup should not add handlers
    unified_logging.setup_logging(
        level=logging.INFO,
        app_name="test-app",
        enable_otel=False,
        enable_console=True,
        force_setup=False,
    )

    # Verify handler count hasn't increased
    assert len(root.handlers) == handler_count


def test_setup_server_logging():
    """Test server logging setup."""
    # Reset specific server loggers
    for logger_name in ["uvicorn", "uvicorn.error", "uvicorn.access"]:
        logger = logging.getLogger(logger_name)
        logger.handlers.clear()

    # Setup server logging
    unified_logging.setup_server_logging(
        app_name="test-app",
        app_type="external-api",
        domain="test",
        app_env="dev",
    )

    # Verify uvicorn logger is configured
    uvicorn_logger = logging.getLogger("uvicorn")
    assert len(uvicorn_logger.handlers) > 0
    assert uvicorn_logger.level == logging.INFO
    assert not uvicorn_logger.propagate


def test_get_gunicorn_config():
    """Test Gunicorn config generation."""
    config = unified_logging.get_gunicorn_config(
        app_name="test-app",
        app_type="external-api",
        domain="test",
        app_env="dev",
        port=8000,
        workers=2,
    )

    # Verify basic config
    assert config["bind"] == "0.0.0.0:8000"
    assert config["workers"] == 2
    assert "test" in config["access_log_format"]
    assert "test-app" in config["access_log_format"]


def test_get_gunicorn_config_minimal():
    """Test Gunicorn config generation with minimal parameters."""
    config = unified_logging.get_gunicorn_config(
        app_name="test-app",
    )

    # Verify basic config
    assert config["bind"] == "0.0.0.0:8000"
    assert config["workers"] == 1
    assert "test-app" in config["access_log_format"]
