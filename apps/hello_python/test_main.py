"""Tests for the hello_python application."""

import pytest
from apps.hello_python.main import get_message

def test_get_message():
    """Test the get_message function."""
    message = get_message()
    assert "Hello, world from uv and Bazel from Python!" in message

def test_get_message_not_empty():
    """Test that get_message returns a non-empty string."""
    assert len(get_message()) > 0
