#!/usr/bin/env python3
"""Block-to-text rendering, which is what gets persisted for a sent message.

This is the plain-text mirror Slack builds for accessibility and for FCM's own
message store, so every block type the bot can post has to survive the round
trip. The V1 status-block renderers that used to be covered here are gone with
manman V1.
"""

import pytest
from slack_sdk.models.blocks import (
    ActionsBlock,
    ButtonElement,
    ContextBlock,
    DividerBlock,
    HeaderBlock,
    ImageBlock,
    InputBlock,
    PlainTextObject,
    SectionBlock,
)

from friendly_computing_machine.src.friendly_computing_machine.bot.slack_models import (
    render_blocks_to_text,
)


def test_empty_input_renders_empty():
    assert render_blocks_to_text([]) == ""


@pytest.mark.parametrize(
    "block, expected",
    [
        (SectionBlock(text={"type": "mrkdwn", "text": "a section"}), "a section"),
        (HeaderBlock(text=PlainTextObject(text="a header", emoji=False)), "# a header"),
        (DividerBlock(), "---"),
        (ImageBlock(image_url="http://example.com/i.png", alt_text="an image"), "[an image]"),
        (
            ActionsBlock(
                elements=[
                    ButtonElement(text=PlainTextObject(text="Go", emoji=False), action_id="go")
                ]
            ),
            "[Go]",
        ),
        (ContextBlock(elements=[PlainTextObject(text="some context")]), "(some context)"),
        (
            InputBlock(
                block_id="b",
                label=PlainTextObject(text="A question", emoji=False),
                element=None,
            ),
            "Input: A question",
        ),
    ],
)
def test_each_block_type_renders_to_text(block, expected):
    assert render_blocks_to_text([block]) == expected


def test_blocks_join_in_order():
    rendered = render_blocks_to_text(
        [
            SectionBlock(text={"type": "mrkdwn", "text": "first"}),
            DividerBlock(),
            SectionBlock(text={"type": "mrkdwn", "text": "second"}),
        ]
    )

    assert rendered.splitlines() == ["first", "---", "second"]
