"""
Shared pytest fixtures for release helper tests.

This module provides common fixtures that reduce boilerplate across test files.
"""

import os
import pytest
from unittest.mock import patch


@pytest.fixture
def mock_print():
    """Mock builtins.print to avoid output during tests."""
    with patch('builtins.print') as mock:
        yield mock


@pytest.fixture
def clean_environ():
    """Clear environment variables for clean test state."""
    with patch.dict(os.environ, {}, clear=True):
        yield


@pytest.fixture
def github_owner_env():
    """Mock GITHUB_REPOSITORY_OWNER environment variable."""
    with patch.dict(os.environ, {"GITHUB_REPOSITORY_OWNER": "TestOwner"}):
        yield


@pytest.fixture
def github_actions_env():
    """Mock GITHUB_ACTIONS environment variable for CI scenarios."""
    with patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}):
        yield


@pytest.fixture
def build_workspace_env():
    """Mock BUILD_WORKSPACE_DIRECTORY environment variable."""
    test_path = "/workspace/build/dir"
    with patch.dict(os.environ, {"BUILD_WORKSPACE_DIRECTORY": test_path}):
        yield test_path


@pytest.fixture
def mock_subprocess_run():
    """Mock subprocess.run for command execution tests."""
    with patch('subprocess.run') as mock:
        yield mock


@pytest.fixture
def sample_apps():
    """Common sample app data used across multiple test files."""
    return [
        {
            "name": "hello_python",
            "domain": "demo",
            "bazel_target": "//demo/hello_python:hello_python_metadata"
        },
        {
            "name": "hello_go", 
            "domain": "demo",
            "bazel_target": "//demo/hello_go:hello_go_metadata"
        },
        {
            "name": "hello_fastapi",
            "domain": "demo", 
            "bazel_target": "//demo/hello_fastapi:hello_fastapi_metadata"
        },
        {
            "name": "status_service",
            "domain": "api",
            "bazel_target": "//api/status_service:status_service_metadata"
        }
    ]


@pytest.fixture
def sample_metadata():
    """Common sample metadata used across multiple test files."""
    return {
        "name": "hello_python",
        "domain": "demo",
        "registry": "ghcr.io",
        "version": "latest"
    }