"""
Unit tests for the status API endpoints.

Tests the core functionality:
- GET /status/worker/{worker_id} - returns status or 404
- GET /status/instance/{game_server_instance_id} - returns status or 404
"""

from unittest.mock import Mock, patch

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from manman.src.host.api.status.api import router
from manman.src.models import ExternalStatusInfo, StatusType


@pytest.fixture
def client():
    """Create a test client for the status API router."""
    app = FastAPI()
    app.include_router(router)
    return TestClient(app)


@pytest.fixture
def mock_status_repository():
    """
    Create a mock StatusRepository that can be easily configured for tests.

    This fixture provides a clean, reusable way to mock the StatusRepository
    without repeating the patch setup in every test.

    Note: This could also be imported from conftest.py for even better reusability:
    # from tests.conftest import create_mock_repository
    # yield from create_mock_repository('manman.src.host.api.status.api.StatusRepository')
    """
    with patch("manman.src.host.api.status.api.StatusRepository") as mock_repo_class:
        mock_repo = Mock()
        mock_repo_class.return_value = mock_repo
        yield mock_repo


class TestWorkerStatusEndpoint:
    """Test GET /status/worker/{worker_id}"""

    def test_returns_worker_status_when_found(self, client, mock_status_repository):
        """Test: Pass in worker ID, get back worker status."""
        # Arrange
        expected_status = ExternalStatusInfo.create(
            class_name="WorkerService", status_type=StatusType.RUNNING, worker_id=123
        )
        mock_status_repository.get_latest_worker_status.return_value = expected_status

        # Act
        response = client.get("/status/worker/123")

        # Assert
        assert response.status_code == 200
        data = response.json()
        assert data["worker_id"] == 123
        assert data["status_type"] == "RUNNING"
        mock_status_repository.get_latest_worker_status.assert_called_once_with(123)

    def test_returns_404_when_worker_not_found(self, client, mock_status_repository):
        """Test: Pass in unknown worker ID, get 404."""
        # Arrange
        mock_status_repository.get_latest_worker_status.return_value = None

        # Act
        response = client.get("/status/worker/999")

        # Assert
        assert response.status_code == 404
        assert response.json()["detail"] == "Worker not found"
        mock_status_repository.get_latest_worker_status.assert_called_once_with(999)


class TestInstanceStatusEndpoint:
    """Test GET /status/instance/{game_server_instance_id}"""

    def test_returns_instance_status_when_found(self, client, mock_status_repository):
        """Test: Pass in instance ID, get back instance status."""
        # Arrange
        expected_status = ExternalStatusInfo.create(
            class_name="Server",
            status_type=StatusType.RUNNING,
            game_server_instance_id=456,
        )
        mock_status_repository.get_latest_instance_status.return_value = expected_status

        # Act
        response = client.get("/status/instance/456")

        # Assert
        assert response.status_code == 200
        data = response.json()
        assert data["game_server_instance_id"] == 456
        assert data["status_type"] == "RUNNING"
        mock_status_repository.get_latest_instance_status.assert_called_once_with(456)

    def test_returns_404_when_instance_not_found(self, client, mock_status_repository):
        """Test: Pass in unknown instance ID, get 404."""
        # Arrange
        mock_status_repository.get_latest_instance_status.return_value = None

        # Act
        response = client.get("/status/instance/999")

        # Assert
        assert response.status_code == 404
        assert response.json()["detail"] == "Instance not found"
        mock_status_repository.get_latest_instance_status.assert_called_once_with(999)
