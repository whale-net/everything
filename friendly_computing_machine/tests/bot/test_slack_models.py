"""Test suite for Slack model utilities."""

from datetime import datetime

from manman.external.manman.status_api.models.external_status_info import ExternalStatusInfo
from manman.external.manman.status_api.models.status_type import StatusType
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_models import (
    create_worker_status_blocks,
    render_blocks_to_text,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackSpecialChannelType,
)


def test_create_and_render_worker_status_blocks():
    """Test creating worker status blocks and rendering them to text."""
    worker_name = "Test Worker"
    worker_id = "worker-123"

    # Create test objects
    special_channel_type = SlackSpecialChannelType(
        id=1, type_name="test_worker", friendly_type_name=worker_name
    )

    current_status = ExternalStatusInfo(
        class_name="TestWorker",
        status_info_id=1,
        status_type=StatusType.RUNNING,
        worker_id=int(worker_id.split("-")[1]),
        as_of=datetime.now(),
    )

    # Create worker status blocks
    blocks = create_worker_status_blocks(special_channel_type, current_status)

    assert blocks is not None
    assert len(blocks) > 0, "No blocks were created"

    # Render to text
    text_output = render_blocks_to_text(blocks)
    assert text_output is not None
    assert isinstance(text_output, str)
    assert len(text_output.strip()) > 0, "Rendered text is empty"
    assert worker_name in text_output
    assert str(current_status.worker_id) in text_output
    assert "RUNNING" in text_output

    print(f"Rendered text for verification:\n{text_output}")
