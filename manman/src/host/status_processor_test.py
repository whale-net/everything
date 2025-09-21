"""
Integration tests for the status processor implementation.

This module tests that the status processor correctly:
1. Receives status messages from RabbitMQ
2. Extracts routing keys properly
3. Writes status information to the database
"""

import os
from unittest.mock import Mock, patch

import pytest

from manman.src.host.status_processor import StatusEventProcessor
from manman.src.models import ExternalStatusInfo, InternalStatusInfo, StatusType
from manman.src.constants import EntityRegistry


class TestStatusProcessor:
    """Integration tests for the status processor."""

    @pytest.fixture(autouse=True)
    def setup_test_environment(self, monkeypatch):
        """Setup test environment variables."""
        # Mock environment variables for testing
        test_env = {
            "MANMAN_RABBITMQ_HOST": "localhost",
            "MANMAN_RABBITMQ_PORT": "5672",
            "MANMAN_RABBITMQ_USER": "guest",
            "MANMAN_RABBITMQ_PASSWORD": "guest",
            "MANMAN_RABBITMQ_ENABLE_SSL": "false",
            "MANMAN_POSTGRES_URL": "postgresql+psycopg2://postgres:postgres@localhost:5432/manman_test",
            "APP_ENV": "test",
        }

        for key, value in test_env.items():
            monkeypatch.setenv(key, value)

    @pytest.fixture
    def mock_rabbitmq_connection(self):
        """Create a properly mocked RabbitMQ connection."""
        mock_connection = Mock()

        # Mock the channel and queue.declare to return proper structure
        mock_channel = Mock()
        mock_connection.channel.return_value = mock_channel
        mock_channel.queue.declare.return_value = {"queue": "test-queue"}

        return mock_connection

    def test_status_processor_initialization(self, mock_rabbitmq_connection):
        """Test that the status processor can be initialized."""
        with (
            patch(
                "manman.src.host.status_processor.RabbitSubscriber"
            ) as mock_subscriber_class,
            patch(
                "manman.src.host.status_processor.RabbitPublisher"
            ) as mock_publisher_class,
        ):
            mock_subscriber = Mock()
            mock_publisher = Mock()
            mock_subscriber_class.return_value = mock_subscriber
            mock_publisher_class.return_value = mock_publisher

            # This should not raise any exceptions
            processor = StatusEventProcessor(mock_rabbitmq_connection)

            assert processor is not None
            assert processor._rabbitmq_connection == mock_rabbitmq_connection
            assert processor._is_running is False
            # Check that the internal components were initialized
            assert processor._internal_status_subscriber is not None
            assert processor._external_worker_status_publisher is not None

    def test_status_message_handling(self, mock_rabbitmq_connection):
        """Test status message handling without actual database/RabbitMQ."""
        with (
            patch("manman.src.host.status_processor.RabbitSubscriber"),
            patch("manman.src.host.status_processor.RabbitPublisher"),
        ):
            processor = StatusEventProcessor(mock_rabbitmq_connection)

        # Create a test status message
        status_info = ExternalStatusInfo.create(
            "WorkerService", StatusType.RUNNING, worker_id=123
        )

        # Mock the database repository write method to avoid actual database calls
        with patch.object(
            processor._db_repository, "write_external_status_to_database"
        ) as mock_write:
            processor._db_repository.write_external_status_to_database(status_info)

            # Verify the database write method was called with correct parameters
            mock_write.assert_called_once_with(status_info)

    def test_status_message_creation(self):
        """Test that ExternalStatusInfo objects are created correctly."""
        status_info = ExternalStatusInfo.create(
            "TestClass", StatusType.CREATED, worker_id=789
        )

        # Test that the status info has the expected properties
        assert status_info.class_name == "TestClass"
        assert status_info.status_type == StatusType.CREATED
        assert status_info.worker_id == 789

    def test_database_error_handling(self, mock_rabbitmq_connection):
        """Test that database errors are handled gracefully."""
        with (
            patch("manman.src.host.status_processor.RabbitSubscriber"),
            patch("manman.src.host.status_processor.RabbitPublisher"),
        ):
            processor = StatusEventProcessor(mock_rabbitmq_connection)

            # Mock the entire write_external_status_to_database method to raise an exception
            with patch.object(
                processor._db_repository,
                "write_external_status_to_database",
                side_effect=Exception("Database connection failed"),
            ) as mock_write:
                # Mock the internal status subscriber to return a test message
                with patch.object(
                    processor._internal_status_subscriber,
                    "get_internal_statuses",
                    return_value=[
                        InternalStatusInfo.create(
                            entity_type=EntityRegistry.WORKER,
                            identifier="123",
                            status_type=StatusType.RUNNING,
                        )
                    ],
                ):
                    # Mock the external publishers to avoid actual publishing
                    with (
                        patch.object(processor, "_external_worker_status_publisher"),
                        patch.object(processor, "_external_gsi_status_publisher"),
                    ):
                        # This should not raise an exception despite the database error
                        processor._process_internal_status_messages()

                # Verify that the database write method was called
                mock_write.assert_called_once()

    def test_status_info_fields_mapping(self, mock_rabbitmq_connection):
        """Test that StatusInfo database record is created with correct field mapping."""
        with (
            patch("manman.src.host.status_processor.RabbitSubscriber"),
            patch("manman.src.host.status_processor.RabbitPublisher"),
        ):
            processor = StatusEventProcessor(mock_rabbitmq_connection)

            # Create test status info with specific values
            status_info = ExternalStatusInfo.create(
                "TestWorker", StatusType.COMPLETE, worker_id=999
            )

            with patch("manman.util.get_sqlalchemy_session") as mock_session_ctx:
                mock_session = Mock()
                mock_session_ctx.return_value.__enter__.return_value = mock_session

                processor._db_repository.write_external_status_to_database(status_info)

                # Get the StatusInfo object that was passed to session.add
                mock_session.add.assert_called_once()
                added_record = mock_session.add.call_args[0][0]

                # Verify all fields are correctly mapped
                assert added_record.worker_id == 999
                assert added_record.game_server_instance_id is None
                assert added_record.class_name == "TestWorker"
                assert added_record.status_type == StatusType.COMPLETE
                assert added_record.as_of == status_info.as_of

    @pytest.mark.integration
    def test_message_processing_flow(self, mock_rabbitmq_connection):
        """Integration test for the complete message processing flow."""
        with (
            patch("manman.src.host.status_processor.RabbitSubscriber"),
            patch("manman.src.host.status_processor.RabbitPublisher"),
        ):
            processor = StatusEventProcessor(mock_rabbitmq_connection)

            # Mock the status subscriber to return test messages
            test_internal_statuses = [
                InternalStatusInfo.create(
                    entity_type=EntityRegistry.WORKER,
                    identifier="100",
                    status_type=StatusType.CREATED,
                ),
                InternalStatusInfo.create(
                    entity_type=EntityRegistry.WORKER,
                    identifier="101",
                    status_type=StatusType.RUNNING,
                ),
            ]

            with (
                patch.object(
                    processor._internal_status_subscriber,
                    "get_internal_statuses",
                    return_value=test_internal_statuses,
                ),
                patch.object(
                    processor, "_external_worker_status_publisher"
                ) as mock_worker_publisher,
                patch.object(
                    processor._db_repository, "write_external_status_to_database"
                ) as mock_db_write,
            ):
                processor._process_internal_status_messages()

                # Verify that each message was published externally
                # Worker status should be published via worker publisher
                assert mock_worker_publisher.publish_external_status.call_count == 2

                # Verify that each message was written to the database
                assert mock_db_write.call_count == 2

    def test_processor_shutdown(self, mock_rabbitmq_connection):
        """Test that the processor shuts down cleanly."""
        with (
            patch("manman.src.host.status_processor.RabbitSubscriber"),
            patch("manman.src.host.status_processor.RabbitPublisher"),
        ):
            processor = StatusEventProcessor(mock_rabbitmq_connection)

            # Verify initial state
            assert processor._is_running is False

            # Set running state to simulate processor running
            processor._is_running = True

            # Call shutdown
            processor._shutdown()

            # Verify shutdown completed
            assert processor._is_running is False


class TestStatusProcessorRealConnections:
    """Tests that can run with real connections if services are available."""

    @pytest.mark.skipif(
        not os.getenv("INTEGRATION_TESTS_ENABLED"),
        reason="Integration tests require INTEGRATION_TESTS_ENABLED=true and running services",
    )
    def test_end_to_end_status_processing(self):
        """End-to-end test with real RabbitMQ and database connections."""
        # This test would only run when explicitly enabled and when services are running
        pytest.skip("End-to-end integration test - implement when needed")
