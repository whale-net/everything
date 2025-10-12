#!/usr/bin/env python3
"""Test script to verify the slack blocks functionality."""

from datetime import datetime

import pytest
from slack_sdk.models.blocks import ActionsBlock

from external.manman_status_api.models.external_status_info import ExternalStatusInfo
from external.manman_status_api.models.status_type import StatusType
from friendly_computing_machine.bot.slack_models import (
    create_worker_status_blocks,
    render_blocks_to_text,
)
from friendly_computing_machine.models.slack import SlackSpecialChannelType


@pytest.mark.parametrize(
    "status, has_buttons",
    [
        (StatusType.RUNNING, True),
        (StatusType.LOST, False),
        (StatusType.CREATED, False),
        (StatusType.COMPLETE, False),
        # INIT intentionally left
    ],
)
def test_blocks(status: StatusType, has_buttons: bool):
    """Test creating blocks and rendering them to text."""
    # Create test objects
    special_channel_type = SlackSpecialChannelType(
        id=1, type_name="test_worker", friendly_type_name="Test Worker"
    )

    current_status = ExternalStatusInfo(
        class_name="TestWorker",
        status_info_id=1,
        status_type=status,
        worker_id=123,
        as_of=datetime.now(),
    )

    # Create worker status blocks
    blocks = create_worker_status_blocks(special_channel_type, current_status)

    has_action_block = any(isinstance(block, ActionsBlock) for block in blocks)

    print("Created blocks:")
    for i, block in enumerate(blocks):
        print(f"  Block {i}: {type(block).__name__}")

    # Render to text
    text_output = render_blocks_to_text(blocks)
    print(f"\nRendered text:\n{text_output}")
    if not text_output:
        raise ValueError("Rendered text is empty")
    if has_buttons:
        assert "Stop" in text_output, "Expected 'Stop' button text in rendered output"
        assert has_action_block
    else:
        assert "Stop" not in text_output, (
            "Did not expect 'Stop' button text in rendered output"
        )
        assert not has_action_block
