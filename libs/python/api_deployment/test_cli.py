"""
Tests for api_deployment.cli module.
"""

import pytest
from libs.python.api_deployment.cli import create_deployment_cli


def test_create_deployment_cli_with_defaults():
    """Test creating CLI with default values."""
    parser = create_deployment_cli(app_module="main:app")
    
    # Parse with no arguments
    args = parser.parse_args([])
    
    assert args.host == "0.0.0.0"
    assert args.port == 8000
    assert args.production is False
    assert args.workers is None
    assert args.timeout == 30
    assert args.log_level == "info"


def test_create_deployment_cli_with_custom_defaults():
    """Test creating CLI with custom default values."""
    parser = create_deployment_cli(
        app_module="custom:app",
        app_name="custom-api",
        default_port=8080,
        description="Custom API Description",
    )
    
    args = parser.parse_args([])
    assert args.port == 8080


def test_create_deployment_cli_with_host_argument():
    """Test CLI with host argument."""
    parser = create_deployment_cli(app_module="main:app")
    args = parser.parse_args(["--host", "127.0.0.1"])
    
    assert args.host == "127.0.0.1"


def test_create_deployment_cli_with_port_argument():
    """Test CLI with port argument."""
    parser = create_deployment_cli(app_module="main:app")
    args = parser.parse_args(["--port", "9000"])
    
    assert args.port == 9000


def test_create_deployment_cli_with_production_flag():
    """Test CLI with production flag."""
    parser = create_deployment_cli(app_module="main:app")
    args = parser.parse_args(["--production"])
    
    assert args.production is True


def test_create_deployment_cli_with_workers_argument():
    """Test CLI with workers argument."""
    parser = create_deployment_cli(app_module="main:app")
    args = parser.parse_args(["--workers", "8"])
    
    assert args.workers == 8


def test_create_deployment_cli_with_timeout_argument():
    """Test CLI with timeout argument."""
    parser = create_deployment_cli(app_module="main:app")
    args = parser.parse_args(["--timeout", "60"])
    
    assert args.timeout == 60


def test_create_deployment_cli_with_log_level_argument():
    """Test CLI with log-level argument."""
    parser = create_deployment_cli(app_module="main:app")
    args = parser.parse_args(["--log-level", "debug"])
    
    assert args.log_level == "debug"


def test_create_deployment_cli_with_all_arguments():
    """Test CLI with all arguments specified."""
    parser = create_deployment_cli(app_module="main:app")
    args = parser.parse_args([
        "--host", "192.168.1.1",
        "--port", "3000",
        "--production",
        "--workers", "4",
        "--timeout", "45",
        "--log-level", "warning",
    ])
    
    assert args.host == "192.168.1.1"
    assert args.port == 3000
    assert args.production is True
    assert args.workers == 4
    assert args.timeout == 45
    assert args.log_level == "warning"


def test_create_deployment_cli_invalid_log_level():
    """Test CLI rejects invalid log level."""
    parser = create_deployment_cli(app_module="main:app")
    
    with pytest.raises(SystemExit):
        parser.parse_args(["--log-level", "invalid"])
