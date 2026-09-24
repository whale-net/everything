"""Unit tests for WhagentClient.latest_assistant_message's from_seq watermark.

Exercises the method directly against a stubbed read_transcript rather
than a real gRPC channel -- WhagentClient.__init__ only builds a lazy
grpc.insecure_channel (no network I/O), so constructing a real instance
and monkeypatching read_transcript is enough.
"""

import json
from types import SimpleNamespace

from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    WhagentClient,
)


def _event(seq: int, turn: int, type_: str, content: str | None = None):
    payload = json.dumps({"role": "assistant", "content": content}) if content is not None else "{}"
    return SimpleNamespace(seq=seq, turn=turn, type=type_, payload=payload)


def _client() -> WhagentClient:
    return WhagentClient(
        api_url="localhost:0",
        keycloak_token_url="http://example.invalid/token",
        client_id="test",
        client_secret="test",
    )


def test_latest_assistant_message_returns_none_when_nothing_past_watermark():
    client = _client()
    events = [
        _event(1, 1, "user_message"),
        _event(2, 1, "assistant_message", "old answer"),
    ]
    client.read_transcript = lambda session_id, from_seq=0: (
        [e for e in events if e.seq >= from_seq],
        3,
    )

    # since_seq is past every event in this window (as it would be right
    # after that turn's answer was already consumed) -- nothing new.
    assert client.latest_assistant_message("s1", from_seq=3) is None


def test_latest_assistant_message_ignores_events_before_the_watermark():
    client = _client()
    events = [
        _event(2, 1, "assistant_message", "previous turn's answer"),
        _event(5, 2, "user_message"),
        _event(6, 2, "assistant_message", "this turn's answer"),
    ]
    client.read_transcript = lambda session_id, from_seq=0: (
        [e for e in events if e.seq >= from_seq],
        7,
    )

    # from_seq=3 excludes seq=2 (already-consumed previous turn) -- only
    # the new turn's message should come back.
    result = client.latest_assistant_message("s1", from_seq=3)

    assert result == ("this turn's answer", 6)


def test_latest_assistant_message_returns_the_last_message_in_a_multi_message_turn():
    client = _client()
    events = [
        _event(6, 2, "assistant_message", "let me check..."),
        _event(7, 2, "tool_call"),
        _event(8, 2, "assistant_message", "the final answer"),
    ]
    client.read_transcript = lambda session_id, from_seq=0: (events, 9)

    result = client.latest_assistant_message("s1", from_seq=3)

    assert result == ("the final answer", 8)
