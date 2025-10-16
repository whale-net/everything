"""
Tests for api_deployment.config module.
"""

import multiprocessing
import pytest
from libs.python.api_deployment.config import get_default_gunicorn_config


def test_get_default_gunicorn_config_with_defaults():
    """Test getting default gunicorn configuration with default values."""
    config = get_default_gunicorn_config()
    
    # Check basic configuration
    assert config["bind"] == "0.0.0.0:8000"
    assert config["worker_class"] == "uvicorn.workers.UvicornWorker"
    assert config["timeout"] == 30
    assert config["keepalive"] == 2
    assert config["max_requests"] == 1000
    assert config["max_requests_jitter"] == 100
    
    # Check logging configuration
    assert config["accesslog"] == "-"
    assert config["errorlog"] == "-"
    assert config["loglevel"] == "info"
    assert config["capture_output"] is True
    
    # Check workers are auto-calculated
    expected_workers = (multiprocessing.cpu_count() * 2) + 1
    assert config["workers"] == expected_workers


def test_get_default_gunicorn_config_with_custom_values():
    """Test getting gunicorn configuration with custom values."""
    config = get_default_gunicorn_config(
        app_name="test-api",
        host="127.0.0.1",
        port=8080,
        workers=4,
        timeout=60,
        log_level="debug",
    )
    
    assert config["bind"] == "127.0.0.1:8080"
    assert config["workers"] == 4
    assert config["timeout"] == 60
    assert config["loglevel"] == "debug"
    assert "test-api" in config["access_log_format"]


def test_get_default_gunicorn_config_with_kwargs():
    """Test that additional kwargs are merged into configuration."""
    config = get_default_gunicorn_config(
        custom_setting="custom_value",
        another_setting=123,
    )
    
    assert config["custom_setting"] == "custom_value"
    assert config["another_setting"] == 123


def test_get_default_gunicorn_config_preload_app():
    """Test that preload_app is set to False by default."""
    config = get_default_gunicorn_config()
    assert config["preload_app"] is False


def test_get_default_gunicorn_config_app_name_in_log_format():
    """Test that app_name is included in the access log format."""
    config = get_default_gunicorn_config(app_name="my-custom-api")
    assert "my-custom-api" in config["access_log_format"]


def test_get_default_gunicorn_config_worker_connections():
    """Test that worker_connections is set correctly."""
    config = get_default_gunicorn_config()
    assert config["worker_connections"] == 1000


def test_get_default_gunicorn_config_with_zero_workers():
    """Test that workers=0 is respected (useful for debugging)."""
    config = get_default_gunicorn_config(workers=0)
    assert config["workers"] == 0


def test_get_default_gunicorn_config_with_different_worker_class():
    """Test using a different worker class."""
    config = get_default_gunicorn_config(
        worker_class="some.other.WorkerClass"
    )
    assert config["worker_class"] == "some.other.WorkerClass"
