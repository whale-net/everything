"""Tests for manman core configuration and models."""

import pytest
import sys
import os

# Add the parent directory to the path so we can import the modules
sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from config import ManManConfig


def test_api_config_validation():
    """Test that API configuration validation works correctly."""
    # Test valid API name
    config = ManManConfig.get_api_config(ManManConfig.EXPERIENCE_API)
    assert config.name == ManManConfig.EXPERIENCE_API
    assert config.root_path == "/experience"
    
    # Test invalid API name
    with pytest.raises(ValueError):
        ManManConfig.get_api_config("invalid-api")


def test_service_name_validation():
    """Test that service name validation works correctly.""" 
    # Test valid service name
    name = ManManConfig.validate_service_name(ManManConfig.WORKER)
    assert name == ManManConfig.WORKER
    
    # Test invalid service name
    with pytest.raises(ValueError):
        ManManConfig.validate_service_name("invalid-service")