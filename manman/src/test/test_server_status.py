"""
Tests for server status publishing functionality.

This module tests that the Server class correctly publishes status messages
during its lifecycle.
"""

from unittest.mock import Mock, patch

import pytest

from manman.models import ExternalStatusInfo, StatusType
from manman.repository.rabbitmq.config import EntityRegistry
from manman.worker.server import Server


class TestServerStatusPublishing:
    """Test server status publishing functionality."""

    @pytest.fixture
    def mock_dependencies(self):
        """Create mocked dependencies for Server initialization."""
        mock_wapi = Mock()
        mock_rabbitmq_connection = Mock()
        mock_config = Mock()
        mock_config.game_server_id = 1
        mock_config.name = "test-server"
        mock_config.executable = "server.exe"
        mock_config.args = ["--port", "27015"]
        mock_config.env_var = []

        # Mock the instance creation
        mock_instance = Mock()
        mock_instance.game_server_instance_id = 123
        mock_wapi.game_server_instance_create.return_value = mock_instance

        # Mock game server
        mock_game_server = Mock()
        mock_game_server.server_type = 1  # STEAM
        mock_game_server.app_id = 730
        mock_wapi.game_server.return_value = mock_game_server

        return {
            "wapi": mock_wapi,
            "rabbitmq_connection": mock_rabbitmq_connection,
            "config": mock_config,
            "instance": mock_instance,
            "game_server": mock_game_server,
        }

    @patch("manman.worker.server.ManManService._ManManService__build_status_publisher")
    @patch("manman.worker.server.ManManService._ManManService__build_command_consumer")
    @patch("manman.worker.server.ProcessBuilder")
    def test_server_initialization_publishes_created_status(
        self, mock_pb, mock_cmd_consumer, mock_status_pub_builder, mock_dependencies
    ):
        """Test that server initialization publishes CREATED status."""
        mock_status_pub_service = Mock()
        mock_status_pub_builder.return_value = mock_status_pub_service

        mock_cmd_service = Mock()
        mock_cmd_consumer.return_value = mock_cmd_service

        # Create a Server instance to trigger the status publishing
        Server(
            wapi=mock_dependencies["wapi"],
            rabbitmq_connection=mock_dependencies["rabbitmq_connection"],
            root_install_directory="/test/path",
            config=mock_dependencies["config"],
            worker_id=123,
        )

        # Verify status publisher service was created and used
        mock_status_pub_builder.assert_called_once()
        mock_status_pub_service.publish_status.assert_called_once()

        # Verify CREATED status was published
        call_args = mock_status_pub_service.publish_status.call_args
        published_status = call_args[1]["internal_status"]

        assert published_status.entity_type == EntityRegistry.GAME_SERVER_INSTANCE
        assert published_status.status_type == StatusType.CREATED
        assert published_status.identifier == "123"  # game_server_instance_id as string

    @patch("manman.worker.server.ManManService._ManManService__build_status_publisher")
    @patch("manman.worker.server.ManManService._ManManService__build_command_consumer")
    @patch("manman.worker.server.ProcessBuilder")
    @patch("manman.worker.server.SteamCMD")
    @patch("manman.worker.server.env_list_to_dict")
    def test_server_run_publishes_lifecycle_statuses(
        self,
        mock_env_dict,
        mock_steamcmd,
        mock_pb,
        mock_cmd_consumer,
        mock_status_pub_builder,
        mock_dependencies,
    ):
        """Test that server initialization and service setup publishes correct statuses."""
        mock_status_pub_service = Mock()
        mock_status_pub_builder.return_value = mock_status_pub_service

        mock_cmd_service = Mock()
        mock_cmd_service.get_commands.return_value = []
        mock_cmd_consumer.return_value = mock_cmd_service

        mock_env_dict.return_value = {}
        mock_steamcmd_instance = Mock()
        mock_steamcmd.return_value = mock_steamcmd_instance

        # Mock ProcessBuilder behavior
        mock_process = Mock()
        mock_process.status = Mock()
        mock_pb.return_value = mock_process

        # Create a Server instance to trigger the status publishing
        Server(
            wapi=mock_dependencies["wapi"],
            rabbitmq_connection=mock_dependencies["rabbitmq_connection"],
            root_install_directory="/test/path",
            config=mock_dependencies["config"],
            worker_id=123,
        )

        # Verify that CREATED status was published during initialization
        mock_status_pub_service.publish_status.assert_called_once()

        published_status = mock_status_pub_service.publish_status.call_args[1][
            "internal_status"
        ]
        assert published_status.entity_type == EntityRegistry.GAME_SERVER_INSTANCE
        assert published_status.status_type == StatusType.CREATED
        assert published_status.identifier == "123"  # game_server_instance_id as string

    @patch("manman.worker.server.ManManService._ManManService__build_status_publisher")
    @patch("manman.worker.server.ManManService._ManManService__build_command_consumer")
    @patch("manman.worker.server.ProcessBuilder")
    def test_server_shutdown_publishes_complete_status(
        self, mock_pb, mock_cmd_consumer, mock_status_pub_builder, mock_dependencies
    ):
        """Test that server instance data gets updated during shutdown."""
        mock_status_pub_service = Mock()
        mock_status_pub_builder.return_value = mock_status_pub_service

        mock_cmd_service = Mock()
        mock_cmd_consumer.return_value = mock_cmd_service

        # Mock ProcessBuilder
        mock_process = Mock()
        mock_pb.return_value = mock_process

        # Create server instance
        server = Server(
            wapi=mock_dependencies["wapi"],
            rabbitmq_connection=mock_dependencies["rabbitmq_connection"],
            root_install_directory="/tmp/test",
            config=mock_dependencies["config"],
            worker_id=1,
        )

        # Reset mock to clear CREATED status call
        mock_status_pub_service.reset_mock()
        mock_dependencies["wapi"].reset_mock()

        # Mock instance to be not shutdown initially
        server._instance.end_date = None

        # Store the original instance for verification
        original_instance = server._instance

        # Call shutdown
        server._shutdown()

        # Verify that the API client was called to shutdown the instance
        mock_dependencies["wapi"].game_server_instance_shutdown.assert_called_once_with(
            original_instance
        )

        # Verify that the process was stopped
        mock_process.stop.assert_called_once()

    def test_status_info_creation(self):
        """Test that StatusInfo objects are created correctly for servers."""
        status = ExternalStatusInfo.create(
            "Server", StatusType.RUNNING, game_server_instance_id=456
        )

        assert status.class_name == "Server"
        assert status.status_type == StatusType.RUNNING
        assert status.game_server_instance_id == 456
        assert status.worker_id is None
        assert status.as_of is not None
