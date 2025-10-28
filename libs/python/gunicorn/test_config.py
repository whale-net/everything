"""Tests for gunicorn configuration and logging integration."""

import os
import logging
from unittest.mock import Mock, patch, MagicMock

import pytest

from libs.python.gunicorn import get_gunicorn_config, UvicornWorker, UVICORN_AVAILABLE
from libs.python.gunicorn.config import _configure_worker_logging


def test_get_gunicorn_config_defaults():
    """Test that get_gunicorn_config returns expected defaults."""
    config = get_gunicorn_config(microservice_name="test-api")
    
    assert config["bind"] == "0.0.0.0:8000"
    assert config["workers"] == 4  # Increased default
    assert config["threads"] == 2  # New default
    assert config["worker_class"] == "libs.python.gunicorn.uvicorn_worker.UvicornWorker"
    assert config["preload_app"] is True
    assert config["max_requests"] == 1000
    assert config["timeout"] == 120  # Increased default
    assert config["keepalive"] == 5  # Increased default
    assert config["post_fork"] == _configure_worker_logging
    assert "[test-api]" in config["access_log_format"]


def test_get_gunicorn_config_custom_values():
    """Test that get_gunicorn_config accepts custom values."""
    config = get_gunicorn_config(
        microservice_name="custom-api",
        port=9000,
        workers=4,
        worker_class="custom.Worker",
        preload_app=False,
    )
    
    assert config["bind"] == "0.0.0.0:9000"
    assert config["workers"] == 4
    assert config["worker_class"] == "custom.Worker"
    assert config["preload_app"] is False
    assert "[custom-api]" in config["access_log_format"]


@patch.dict(os.environ, {
    "LOG_OTLP": "true",
    "LOG_LEVEL": "DEBUG",
    "LOG_JSON_FORMAT": "false",
    "APP_NAME": "test-app",
})
@patch("libs.python.gunicorn.config.configure_logging")
@patch("libs.python.gunicorn.config.is_configured")
def test_configure_worker_logging_when_not_configured(mock_is_configured, mock_configure_logging):
    """Test that _configure_worker_logging calls configure_logging when not configured."""
    mock_is_configured.return_value = False
    
    server_mock = Mock()
    worker_mock = Mock()
    worker_mock.pid = 12345
    
    _configure_worker_logging(server_mock, worker_mock)
    
    # Should call configure_logging
    mock_configure_logging.assert_called_once()
    call_kwargs = mock_configure_logging.call_args[1]
    assert call_kwargs["log_level"] == "DEBUG"
    assert call_kwargs["enable_otlp"] is True
    assert call_kwargs["json_format"] is False
    assert call_kwargs["force_reconfigure"] is False


@patch("libs.python.gunicorn.config.configure_logging")
@patch("libs.python.gunicorn.config.is_configured")
def test_configure_worker_logging_when_already_configured(mock_is_configured, mock_configure_logging):
    """Test that _configure_worker_logging skips when already configured."""
    mock_is_configured.return_value = True
    
    server_mock = Mock()
    worker_mock = Mock()
    
    _configure_worker_logging(server_mock, worker_mock)
    
    # Should not call configure_logging
    mock_configure_logging.assert_not_called()


@patch.dict(os.environ, {}, clear=True)
@patch("libs.python.gunicorn.config.configure_logging")
@patch("libs.python.gunicorn.config.is_configured")
def test_configure_worker_logging_with_defaults(mock_is_configured, mock_configure_logging):
    """Test that _configure_worker_logging uses defaults when env vars not set."""
    mock_is_configured.return_value = False
    
    server_mock = Mock()
    worker_mock = Mock()
    worker_mock.pid = 12345
    
    _configure_worker_logging(server_mock, worker_mock)
    
    # Should call configure_logging with defaults
    mock_configure_logging.assert_called_once()
    call_kwargs = mock_configure_logging.call_args[1]
    assert call_kwargs["log_level"] == "INFO"  # Default
    assert call_kwargs["enable_otlp"] is False  # Default
    assert call_kwargs["json_format"] is False  # Default


@pytest.mark.skipif(not UVICORN_AVAILABLE, reason="uvicorn not available")
def test_uvicorn_worker_class_exists():
    """Test that UvicornWorker class is available when uvicorn is installed."""
    assert UvicornWorker is not None
    assert hasattr(UvicornWorker, "CONFIG_KWARGS")
    assert UvicornWorker.CONFIG_KWARGS.get("log_config") is None


@pytest.mark.skipif(UVICORN_AVAILABLE, reason="test requires uvicorn to be unavailable")
def test_uvicorn_worker_unavailable():
    """Test that UvicornWorker is None when uvicorn is not available."""
    assert UvicornWorker is None
    assert UVICORN_AVAILABLE is False
