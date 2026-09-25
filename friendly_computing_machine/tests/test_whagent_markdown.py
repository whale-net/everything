"""Agent replies are posted to Slack as markdown blocks so Markdown renders."""

import asyncio

from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent import (
    activity as activity_mod,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.activity import (
    SLACK_MARKDOWN_BLOCK_MAX_CHARS,
    UpdateSlackMessageParams,
    markdown_blocks,
)


def test_markdown_blocks_wraps_text_in_a_markdown_block():
    assert markdown_blocks("**bold**\n- item") == [
        {"type": "markdown", "text": "**bold**\n- item"}
    ]


def test_markdown_blocks_returns_none_past_slack_limit():
    assert markdown_blocks("x" * (SLACK_MARKDOWN_BLOCK_MAX_CHARS + 1)) is None


def test_markdown_blocks_returns_none_for_empty_text():
    assert markdown_blocks("") is None


def _run_update(monkeypatch, params):
    calls = []

    def fake_update(channel, ts, text, blocks=None):
        calls.append((channel, ts, text, blocks))
        return ts

    monkeypatch.setattr(activity_mod, "slack_update_message", fake_update)
    asyncio.run(activity_mod.update_slack_message_activity(params))
    return calls[0]


def test_update_activity_sends_markdown_block_when_requested(monkeypatch):
    _, _, text, blocks = _run_update(
        monkeypatch,
        UpdateSlackMessageParams(channel_id="C1", ts="1.2", text="# hi", markdown=True),
    )
    assert text == "# hi"
    assert blocks == [{"type": "markdown", "text": "# hi"}]


def test_update_activity_defaults_to_plain_text(monkeypatch):
    _, _, _, blocks = _run_update(
        monkeypatch, UpdateSlackMessageParams(channel_id="C1", ts="1.2", text="# hi")
    )
    assert blocks is None


def test_update_activity_falls_back_to_plain_text_when_too_long(monkeypatch):
    long_text = "x" * (SLACK_MARKDOWN_BLOCK_MAX_CHARS + 1)
    _, _, text, blocks = _run_update(
        monkeypatch,
        UpdateSlackMessageParams(channel_id="C1", ts="1.2", text=long_text, markdown=True),
    )
    assert text == long_text
    assert blocks is None
