"""
Tests for the database repository functionality.

This module tests the DatabaseRepository class methods for
status and worker management operations.
"""

from datetime import datetime, timedelta, timezone
from unittest.mock import Mock, patch

import pytest

from manman.src.models import ExternalStatusInfo, StatusType
from manman.src.repository.database import DatabaseRepository


class TestDatabaseRepository:
    """Tests for the DatabaseRepository class."""

    @pytest.fixture
    def mock_session(self):
        """Create a mock database session."""
        session = Mock()
        session.exec.return_value.all.return_value = []
        return session

    @pytest.fixture
    def repository(self):
        """Create a DatabaseRepository instance."""
        return DatabaseRepository()

    @patch("manman.util.get_sqlalchemy_session")
    def test_get_workers_with_stale_heartbeats_default_params(
        self, mock_get_session, repository, mock_session
    ):
        """Test getting workers with stale heartbeats using default parameters."""
        mock_get_session.return_value.__enter__.return_value = mock_session

        # Call the method
        result = repository.get_workers_with_stale_heartbeats()

        # Verify session was used
        mock_get_session.assert_called_once()
        mock_session.exec.assert_called_once()
        assert result == []

    @patch("manman.util.get_sqlalchemy_session")
    def test_get_workers_with_stale_heartbeats_custom_params(
        self, mock_get_session, repository, mock_session
    ):
        """Test getting workers with stale heartbeats using custom parameters."""
        mock_get_session.return_value.__enter__.return_value = mock_session

        # Call the method with custom parameters
        result = repository.get_workers_with_stale_heartbeats(
            heartbeat_threshold_seconds=10, heartbeat_max_lookback_hours=2
        )

        # Verify session was used
        mock_get_session.assert_called_once()
        mock_session.exec.assert_called_once()
        assert result == []

    @patch("manman.util.get_sqlalchemy_session")
    def test_get_active_game_server_instances(
        self, mock_get_session, repository, mock_session
    ):
        """Test getting active game server instances for a worker."""
        mock_get_session.return_value.__enter__.return_value = mock_session
        worker_id = 123

        # Call the method
        result = repository.get_active_game_server_instances(worker_id)

        # Verify session was used
        mock_get_session.assert_called_once()
        mock_session.exec.assert_called_once()
        assert result == []

    @patch("manman.util.get_sqlalchemy_session")
    def test_write_status_to_database(self, mock_get_session, repository, mock_session):
        """Test writing status information to the database."""
        mock_get_session.return_value.__enter__.return_value = mock_session

        # Create a test status info
        status_info = ExternalStatusInfo(
            class_name="TestClass",
            status_type=StatusType.RUNNING,
            as_of=datetime.now(timezone.utc),
            worker_id=123,
        )

        # Call the method
        repository.write_external_status_to_database(status_info)

        # Verify session operations
        mock_get_session.assert_called_once()
        mock_session.add.assert_called_once_with(status_info)
        mock_session.commit.assert_called_once()

    @patch("manman.util.get_sqlalchemy_session")
    def test_get_stale_workers_with_status(
        self, mock_get_session, repository, mock_session
    ):
        """Test getting stale workers with their status using specific timestamps."""
        mock_get_session.return_value.__enter__.return_value = mock_session

        current_time = datetime.now(timezone.utc)
        heartbeat_threshold = current_time - timedelta(seconds=5)
        heartbeat_max_lookback = current_time - timedelta(hours=1)

        # Call the method
        result = repository.get_stale_workers_with_status(
            heartbeat_threshold, heartbeat_max_lookback
        )

        # Verify session was used
        mock_get_session.assert_called_once()
        mock_session.exec.assert_called_once()
        assert result == []

    def test_repository_with_provided_session(self):
        """Test that repository can be initialized with a provided session."""
        mock_session = Mock()
        repository = DatabaseRepository(session=mock_session)

        # The repository should store the provided session
        assert repository._session == mock_session
