"""
Basic tests for the ManMan Subscribe Service
"""

from unittest import mock
from unittest.mock import Mock

from friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.service import (
    ManManSubscribeService,
)


def test_service_initialization():
    """Test that the service can be initialized with required parameters."""
    # Mock the RabbitMQ connection, Slack API, and ManMan Status API
    mock_rabbitmq_connection = Mock()
    mock_slack_api = Mock()
    mock_manman_status_api = Mock()
    app_env = "test"

    with mock.patch(
        "friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.service.get_slack_special_channel_type_from_name",
        return_value=None,
    ):
        service = ManManSubscribeService(
            app_env=app_env,
            rabbitmq_connection=mock_rabbitmq_connection,
            slack_api=mock_slack_api,
            manman_status_api=mock_manman_status_api,
        )

        assert service._rabbitmq_connection == mock_rabbitmq_connection
        assert service._slack_api == mock_slack_api
        assert service._manman_status_api == mock_manman_status_api
        assert service._app_env == app_env
        assert not service._is_running
