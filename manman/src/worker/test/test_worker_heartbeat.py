"""
Test for worker heartbeat functionality including the --heartbeat_length parameter.
"""

import unittest
from datetime import timedelta
from unittest.mock import Mock, patch

from manman.src.worker.worker_service import WorkerService


class TestWorkerHeartbeat(unittest.TestCase):
    """Test worker heartbeat configuration"""

    @patch("manman.worker.worker_service.get_auth_api_client")
    @patch("manman.worker.worker_service.WorkerAPIClient")
    def test_default_heartbeat_length(self, mock_api_client_class, mock_auth_client):
        """Test that WorkerService uses default heartbeat length of 2 seconds"""
        # Mock the API client and worker instance
        mock_api_client = Mock()
        mock_api_client_class.return_value = mock_api_client
        mock_worker_instance = Mock()
        mock_api_client.worker_create.return_value = mock_worker_instance
        mock_api_client.close_other_workers.return_value = None

        # Mock the connection
        mock_connection = Mock()

        # Create WorkerService with default heartbeat length
        service = WorkerService(
            rabbitmq_connection=mock_connection,
            install_directory="/tmp",
            host_url="http://test",
            sa_client_id=None,
            sa_client_secret=None,
        )

        # Verify default heartbeat interval
        self.assertEqual(service.HEARTBEAT_INTERVAL, timedelta(seconds=2))
        self.assertEqual(service._heartbeat_length, 2)

    @patch("manman.worker.worker_service.get_auth_api_client")
    @patch("manman.worker.worker_service.WorkerAPIClient")
    def test_custom_heartbeat_length(self, mock_api_client_class, mock_auth_client):
        """Test that WorkerService uses custom heartbeat length when provided"""
        # Mock the API client and worker instance
        mock_api_client = Mock()
        mock_api_client_class.return_value = mock_api_client
        mock_worker_instance = Mock()
        mock_api_client.worker_create.return_value = mock_worker_instance
        mock_api_client.close_other_workers.return_value = None

        # Mock the connection
        mock_connection = Mock()

        # Create WorkerService with custom heartbeat length
        custom_heartbeat = 10
        service = WorkerService(
            rabbitmq_connection=mock_connection,
            install_directory="/tmp",
            host_url="http://test",
            sa_client_id=None,
            sa_client_secret=None,
            heartbeat_length=custom_heartbeat,
        )

        # Verify custom heartbeat interval
        self.assertEqual(
            service.HEARTBEAT_INTERVAL, timedelta(seconds=custom_heartbeat)
        )
        self.assertEqual(service._heartbeat_length, custom_heartbeat)

    @patch("manman.worker.worker_service.get_auth_api_client")
    @patch("manman.worker.worker_service.WorkerAPIClient")
    def test_heartbeat_length_simulation_lost_condition(
        self, mock_api_client_class, mock_auth_client
    ):
        """Test that heartbeat length > 5 seconds can simulate LOST conditions"""
        # Mock the API client and worker instance
        mock_api_client = Mock()
        mock_api_client_class.return_value = mock_api_client
        mock_worker_instance = Mock()
        mock_api_client.worker_create.return_value = mock_worker_instance
        mock_api_client.close_other_workers.return_value = None

        # Mock the connection
        mock_connection = Mock()

        # Create WorkerService with heartbeat length > 5 seconds
        # This should allow simulation of LOST conditions since the default
        # heartbeat threshold is 5 seconds in get_workers_with_stale_heartbeats()
        heartbeat_length = 7
        service = WorkerService(
            rabbitmq_connection=mock_connection,
            install_directory="/tmp",
            host_url="http://test",
            sa_client_id=None,
            sa_client_secret=None,
            heartbeat_length=heartbeat_length,
        )

        # Verify that the heartbeat interval is longer than the default threshold
        self.assertEqual(service.HEARTBEAT_INTERVAL, timedelta(seconds=7))
        self.assertEqual(service._heartbeat_length, 7)
        # This configuration should allow workers to be marked as LOST
        # since heartbeats will be sent every 7 seconds but the threshold is 5 seconds


if __name__ == "__main__":
    unittest.main()
