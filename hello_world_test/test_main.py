"""Tests for the hello_world_test application."""

import pytest
from hello_world_test.main import get_message

def test_get_message():
    """Test the get_message function."""
    message = get_message()
    assert "Hello, World Test App from Python!" in message

def test_get_message_not_empty():
    """Test that get_message returns a non-empty string."""
    assert len(get_message()) > 0