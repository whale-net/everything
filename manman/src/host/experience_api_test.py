"""
Unit tests for Experience API endpoints.

Tests the worker shutdown endpoint functionality.
"""

from datetime import datetime
from unittest.mock import Mock

import pytest
from fastapi import FastAPI, HTTPException
from fastapi.testclient import TestClient

from manman.src.host.api.experience.api import router
from manman.src.models import Command, CommandType, Worker


@pytest.fixture
def mock_current_worker():
    """Create a mock current worker dependency."""
    return Worker(
        worker_id=123,
        created_date=datetime(2024, 1, 1, 0, 0, 0),
        end_date=None,
        last_heartbeat=None,
    )


@pytest.fixture
def mock_worker_command_pub_service():
    """Create a mock worker command pub service."""
    return Mock()


@pytest.fixture
def app_with_overrides(mock_current_worker, mock_worker_command_pub_service):
    """Create a test FastAPI app with dependency overrides."""
    from manman.src.host.api.shared.injectors import (
        current_worker,
        worker_command_pub_service,
    )

    app = FastAPI()
    app.include_router(router)

    # Override dependencies
    app.dependency_overrides[current_worker] = lambda: mock_current_worker
    app.dependency_overrides[worker_command_pub_service] = (
        lambda: mock_worker_command_pub_service
    )

    return app


@pytest.fixture
def client(app_with_overrides):
    """Create a test client with dependency overrides."""
    return TestClient(app_with_overrides)


class TestWorkerShutdown:
    """Test POST /worker/shutdown endpoint."""

    def test_worker_shutdown_success(self, client, mock_worker_command_pub_service):
        """Test successful worker shutdown command."""
        # Act
        response = client.post("/worker/shutdown")

        # Assert
        assert response.status_code == 200
        response_data = response.json()
        assert response_data["status"] == "success"
        assert "Shutdown command sent to worker 123" in response_data["message"]

        # Verify the command was published
        mock_worker_command_pub_service.publish_command.assert_called_once()

        # Verify the command is correct
        published_command = mock_worker_command_pub_service.publish_command.call_args[
            0
        ][0]
        assert isinstance(published_command, Command)
        assert published_command.command_type == CommandType.STOP
        assert published_command.command_args == []


class TestWorkerShutdownNoWorker:
    """Test POST /worker/shutdown endpoint when no worker is available."""

    def test_worker_shutdown_no_current_worker(self):
        """Test worker shutdown when no current worker exists."""
        from manman.src.host.api.shared.injectors import (
            current_worker,
            worker_command_pub_service,
        )

        app = FastAPI()
        app.include_router(router)

        # Override current_worker to raise HTTPException
        def mock_current_worker_raises():
            raise HTTPException(status_code=404, detail="Worker not found")

        app.dependency_overrides[current_worker] = mock_current_worker_raises
        app.dependency_overrides[worker_command_pub_service] = lambda: Mock()

        client = TestClient(app)

        # Act
        response = client.post("/worker/shutdown")

        # Assert
        assert response.status_code == 404
        assert response.json()["detail"] == "Worker not found"
