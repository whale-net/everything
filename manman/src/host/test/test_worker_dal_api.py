"""
Unit tests for Worker DAL API endpoints.

Tests the core functionality: "Do I pass it in? Do I get something out?"
Focuses on happy path and error cases for worker management endpoints.
"""

from datetime import datetime
from unittest.mock import Mock

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from manman.host.api.worker_dal.worker import router
from manman.models import Worker


@pytest.fixture
def mock_rmq_connection():
    """Create a mock RabbitMQ connection."""
    return Mock()


@pytest.fixture
def client(mock_rmq_connection):
    """Create a test client for the worker DAL API router."""
    from manman.host.api.shared.injectors import rmq_conn

    app = FastAPI()
    app.include_router(router)

    # Override the RabbitMQ connection dependency
    app.dependency_overrides[rmq_conn] = lambda: mock_rmq_connection

    return TestClient(app)


class TestWorkerCreate:
    """Test POST /worker/create endpoint."""

    def test_worker_create_success(self, client, mock_worker_repository):
        """Test successful worker creation."""
        # Arrange
        expected_worker = Worker(
            worker_id=123,
            created_date=datetime(2024, 1, 1, 0, 0, 0),
            end_date=None,
            last_heartbeat=None,
        )
        mock_worker_repository.create_worker.return_value = expected_worker

        # Act
        response = client.post("/worker/create")

        # Assert
        assert response.status_code == 200
        response_data = response.json()
        assert response_data["worker_id"] == 123
        mock_worker_repository.create_worker.assert_called_once()


class TestWorkerShutdown:
    """Test PUT /worker/shutdown endpoint."""

    def test_worker_shutdown_success(self, client, mock_worker_repository):
        """Test successful worker shutdown."""
        # Arrange
        worker_input = {"worker_id": 123}
        expected_worker = Worker(
            worker_id=123,
            created_date=datetime(2024, 1, 1, 0, 0, 0),
            end_date=datetime(2024, 1, 2, 0, 0, 0),
            last_heartbeat=None,
        )
        mock_worker_repository.shutdown_worker.return_value = expected_worker

        # Act
        response = client.put("/worker/shutdown", json=worker_input)

        # Assert
        assert response.status_code == 200
        response_data = response.json()
        assert response_data["worker_id"] == 123
        mock_worker_repository.shutdown_worker.assert_called_once_with(123)

    def test_worker_shutdown_not_found(self, client, mock_worker_repository):
        """Test worker shutdown when worker not found."""
        # Arrange
        worker_input = {"worker_id": 999}
        mock_worker_repository.shutdown_worker.return_value = None

        # Act
        response = client.put("/worker/shutdown", json=worker_input)

        # Assert
        assert response.status_code == 404
        assert response.json()["detail"] == "Worker not found"
        mock_worker_repository.shutdown_worker.assert_called_once_with(999)


class TestWorkerShutdownOther:
    """Test PUT /worker/shutdown/other endpoint."""

    def test_worker_shutdown_other_success(self, client, mock_worker_repository):
        """Test successful shutdown of other workers."""
        # Arrange
        worker_input = {"worker_id": 123}
        mock_worker_repository.close_other_workers.return_value = []

        # Act
        response = client.put("/worker/shutdown/other", json=worker_input)

        # Assert
        assert response.status_code == 200
        mock_worker_repository.close_other_workers.assert_called_once_with(123)

    # TODO lost worker list test


# TODO make work
# check for heartbeat to work
# TBH, this should probably just be a queue
# check that we can't heartbeat dead ones
# class TestWorkerHeartbeat:
#     """Test GET /worker/heartbeat endpoint."""
