"""
Tests for worker shutdown cascade functionality.

This module tests that the WorkerService correctly cascades shutdown signals
to its dependent servers and waits for them to complete before shutting down itself.
"""

import threading
import time
import unittest
from unittest.mock import Mock, patch

from manman.src.worker.worker_service import WorkerService


class TestWorkerShutdownCascade(unittest.TestCase):
    """Test worker shutdown cascade functionality."""

    def setUp(self):
        """Set up common test fixtures."""
        # Mock dependencies
        self.mock_connection = Mock()
        self.mock_wapi = Mock()
        self.mock_worker_instance = Mock()
        self.mock_worker_instance.worker_id = 123

        self.mock_wapi.create_worker.return_value = self.mock_worker_instance
        self.mock_wapi.shutdown_other_workers.return_value = None
        self.mock_wapi.shutdown_worker.return_value = None

    @patch("manman.src.worker.worker_service.get_auth_api_client")
    @patch("manman.src.worker.worker_service.WorkerDALClient")
    def test_shutdown_with_no_servers(self, mock_api_client_class, mock_auth_client):
        """Test worker shutdown when no dependent servers exist."""
        mock_api_client_class.return_value = self.mock_wapi

        # Create WorkerService
        service = WorkerService(
            rabbitmq_connection=self.mock_connection,
            install_directory="/tmp",
            host_url="http://test",
            sa_client_id="test_id",
            sa_client_secret="test_secret",
        )

        # Call shutdown
        service._shutdown()

        # Verify worker API shutdown was called
        self.mock_wapi.shutdown_worker.assert_called_once_with(
            self.mock_worker_instance
        )

    @patch("manman.src.worker.worker_service.get_auth_api_client")
    @patch("manman.src.worker.worker_service.WorkerDALClient")
    def test_shutdown_cascade_to_dependent_servers(
        self, mock_api_client_class, mock_auth_client
    ):
        """Test that worker shutdown cascades to dependent servers."""
        mock_api_client_class.return_value = self.mock_wapi

        # Create WorkerService
        service = WorkerService(
            rabbitmq_connection=self.mock_connection,
            install_directory="/tmp",
            host_url="http://test",
            sa_client_id="test_id",
            sa_client_secret="test_secret",
        )

        # Create mock servers
        mock_server1 = Mock()
        mock_server1.is_shutdown = False
        mock_server1.instance = Mock()
        mock_server1.instance.__str__ = Mock(return_value="server-1")

        mock_server2 = Mock()
        mock_server2.is_shutdown = False
        mock_server2.instance = Mock()
        mock_server2.instance.__str__ = Mock(return_value="server-2")

        # Add servers to the worker
        service._servers = [mock_server1, mock_server2]

        # Set up servers to become shutdown after trigger is called
        def trigger_shutdown_server1():
            mock_server1.is_shutdown = True

        def trigger_shutdown_server2():
            mock_server2.is_shutdown = True

        mock_server1._trigger_internal_shutdown.side_effect = trigger_shutdown_server1
        mock_server2._trigger_internal_shutdown.side_effect = trigger_shutdown_server2

        # Call shutdown
        service._shutdown()

        # Verify shutdown cascade was triggered for both servers
        mock_server1._trigger_internal_shutdown.assert_called_once()
        mock_server2._trigger_internal_shutdown.assert_called_once()

        # Verify worker API shutdown was called after servers
        self.mock_wapi.shutdown_worker.assert_called_once_with(
            self.mock_worker_instance
        )

    @patch("manman.src.worker.worker_service.get_auth_api_client")
    @patch("manman.src.worker.worker_service.WorkerDALClient")
    def test_shutdown_waits_for_server_completion(
        self, mock_api_client_class, mock_auth_client
    ):
        """Test that worker waits for servers to complete shutdown."""
        mock_api_client_class.return_value = self.mock_wapi

        # Create WorkerService
        service = WorkerService(
            rabbitmq_connection=self.mock_connection,
            install_directory="/tmp",
            host_url="http://test",
            sa_client_id="test_id",
            sa_client_secret="test_secret",
        )

        # Create mock server that takes time to shutdown
        mock_server = Mock()
        mock_server.is_shutdown = False
        mock_server.instance = Mock()
        mock_server.instance.__str__ = Mock(return_value="slow-server")

        service._servers = [mock_server]

        shutdown_completed = threading.Event()

        def delayed_shutdown():
            # Simulate server taking time to shutdown
            time.sleep(0.2)  # 200ms delay
            mock_server.is_shutdown = True
            shutdown_completed.set()

        mock_server._trigger_internal_shutdown.side_effect = delayed_shutdown

        # Start shutdown in a separate thread to test timing
        start_time = time.time()

        # Call shutdown - this should wait for the server to complete
        service._shutdown()

        end_time = time.time()

        # Verify that shutdown waited for server completion
        self.assertTrue(shutdown_completed.is_set())
        self.assertGreaterEqual(
            end_time - start_time, 0.2
        )  # Should have waited at least 200ms

        # Verify shutdown was triggered and worker shutdown was called
        mock_server._trigger_internal_shutdown.assert_called_once()
        self.mock_wapi.shutdown_worker.assert_called_once_with(
            self.mock_worker_instance
        )

    @patch("manman.src.worker.worker_service.get_auth_api_client")
    @patch("manman.src.worker.worker_service.WorkerDALClient")
    def test_shutdown_handles_already_shutdown_servers(
        self, mock_api_client_class, mock_auth_client
    ):
        """Test that worker handles servers that are already shutdown."""
        mock_api_client_class.return_value = self.mock_wapi

        # Create WorkerService
        service = WorkerService(
            rabbitmq_connection=self.mock_connection,
            install_directory="/tmp",
            host_url="http://test",
            sa_client_id="test_id",
            sa_client_secret="test_secret",
        )

        # Create mock server that's already shutdown
        mock_server = Mock()
        mock_server.is_shutdown = True  # Already shutdown
        mock_server.instance = Mock()
        mock_server.instance.__str__ = Mock(return_value="already-shutdown-server")

        service._servers = [mock_server]

        # Call shutdown
        service._shutdown()

        # Verify shutdown trigger was NOT called for already shutdown server
        mock_server._trigger_internal_shutdown.assert_not_called()

        # Verify worker API shutdown was still called
        self.mock_wapi.shutdown_worker.assert_called_once_with(
            self.mock_worker_instance
        )


if __name__ == "__main__":
    unittest.main()
