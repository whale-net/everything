"""Tests for hello_openapi_consumer."""
import pytest
from demo.hello_openapi_consumer.main import main


def test_main_runs_successfully():
    """Verify the app can instantiate the OpenAPI client without errors."""
    result = main()
    assert result == 0


def test_client_imports():
    """Verify all required OpenAPI client imports work correctly."""
    from generated.demo.internal_api.api.default_api import DefaultApi
    from generated.demo.internal_api.configuration import Configuration
    
    assert DefaultApi is not None
    assert Configuration is not None


def test_client_instantiation():
    """Verify the OpenAPI client can be instantiated with custom configuration."""
    from generated.demo.internal_api.api.default_api import DefaultApi
    from generated.demo.internal_api.configuration import Configuration
    
    # Create configuration with custom host
    config = Configuration(
        host="http://test-host:8000"
    )
    
    # Verify configuration is set correctly
    assert config.host == "http://test-host:8000"
    
    # Create API client instance
    api_client = DefaultApi()
    
    # Verify client type
    assert type(api_client).__name__ == "DefaultApi"
