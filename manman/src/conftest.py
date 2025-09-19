"""
Shared pytest fixtures and utilities for testing.

This module provides reusable testing utilities that can be used across different test modules.
"""

from typing import Generator
from unittest.mock import Mock, patch

import pytest


def create_mock_repository(repository_import_path: str) -> Generator[Mock, None, None]:
    """
    Factory function to create repository mocks for any repository class.

    This is a reusable utility that can mock any repository by patching its import path.

    Args:
        repository_import_path: The full import path to the repository class
                               (e.g., 'manman.src.host.api.status.api.StatusRepository')

    Returns:
        A mock repository instance that can be configured for tests

    Example usage in a test file:
        @pytest.fixture
        def mock_my_repository():
            yield from create_mock_repository('my.module.MyRepository')
    """
    with patch(repository_import_path) as mock_repo_class:
        mock_repo = Mock()
        mock_repo_class.return_value = mock_repo
        yield mock_repo


# Example fixtures using the factory - add more as needed:


@pytest.fixture
def mock_status_repository():
    """Create a mock StatusRepository for testing status API endpoints."""
    yield from create_mock_repository("manman.src.host.api.status.api.StatusRepository")


@pytest.fixture
def mock_game_repository():
    """Create a mock GameRepository for testing game API endpoints."""
    yield from create_mock_repository("manman.src.host.api.game.api.GameRepository")


@pytest.fixture
def mock_worker_repository():
    """Create a mock WorkerRepository for testing worker DAL API endpoints."""
    yield from create_mock_repository(
        "manman.src.host.api.worker_dal.worker.WorkerRepository"
    )
