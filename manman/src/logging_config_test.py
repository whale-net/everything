"""Tests for logging configuration."""

import pytest

from manman.src.logging_config import get_gunicorn_config


def test_gunicorn_config_default():
    """Test that gunicorn config returns expected default values."""
    config = get_gunicorn_config("test-service")
    
    # Verify basic settings
    assert config["bind"] == "0.0.0.0:8000"
    assert config["workers"] == 1
    assert config["worker_class"] == "uvicorn.workers.UvicornWorker"
    
    # Verify connection and request limits
    assert config["worker_connections"] == 1000
    assert config["max_requests"] == 10000  # Increased from 1000
    assert config["max_requests_jitter"] == 2000  # Increased from 100
    
    # Verify timeout settings
    assert config["keepalive"] == 5  # Increased from 2
    assert config["timeout"] == 30
    assert config["graceful_timeout"] == 30
    
    # Verify logging is configured
    assert config["accesslog"] == "-"
    assert config["errorlog"] == "-"


def test_gunicorn_config_custom_port():
    """Test that custom port is respected."""
    config = get_gunicorn_config("test-service", port=9000)
    assert config["bind"] == "0.0.0.0:9000"


def test_gunicorn_config_custom_workers():
    """Test that custom worker count is respected."""
    config = get_gunicorn_config("test-service", workers=4)
    assert config["workers"] == 4


def test_gunicorn_config_preload_app():
    """Test that preload_app setting is configurable."""
    config = get_gunicorn_config("test-service", preload_app=False)
    assert config["preload_app"] is False
    
    config = get_gunicorn_config("test-service", preload_app=True)
    assert config["preload_app"] is True
