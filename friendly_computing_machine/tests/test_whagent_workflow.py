"""Unit tests for the whagent thread relay's turn-queuing state machine.

This repo has no existing WorkflowEnvironment/time-skipping Temporal test
pattern anywhere (checked friendly_computing_machine/tests and the rest of
the repo), so per AGENTS.md's guidance this exercises the queuing logic
directly rather than inventing a new Temporal test harness: TurnQueue
(temporal/whagent/workflow.py) is a plain, Temporal-free class the
workflow delegates to for exactly this reason.
"""

from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.workflow import (
    SESSION_STATE_CAPPED,
    SESSION_STATE_DONE,
    TurnQueue,
    render_resolved_turn_text,
    workflow_id_for_thread,
)


def test_queue_message_while_idle_is_pending():
    queue = TurnQueue()

    queue.queue_message("hello")

    assert queue.has_pending()


def test_message_arriving_during_in_flight_turn_is_buffered_not_sent():
    queue = TurnQueue()
    queue.turn_in_flight = True

    queue.queue_message("first reply")
    queue.queue_message("second reply")

    # buffered, not drained/sent while the turn is still in flight
    assert queue.has_pending()


def test_drain_combines_and_clears_pending_messages():
    queue = TurnQueue()
    queue.turn_in_flight = True
    queue.queue_message("first reply")
    queue.queue_message("second reply")

    queue.turn_in_flight = False
    combined = queue.drain()

    assert combined == "first reply\nsecond reply"
    assert not queue.has_pending()


def test_drain_with_nothing_queued_returns_none():
    queue = TurnQueue()

    assert queue.drain() is None


def test_drain_is_idempotent_until_more_messages_are_queued():
    queue = TurnQueue()
    queue.queue_message("only message")

    first_drain = queue.drain()
    second_drain = queue.drain()

    assert first_drain == "only message"
    assert second_drain is None


def test_workflow_id_for_thread_is_deterministic():
    first = workflow_id_for_thread("dev", "C123", "1712345678.123456")
    second = workflow_id_for_thread("dev", "C123", "1712345678.123456")

    assert first == second
    assert first == "fcm-dev-whagent-thread-C123-1712345678.123456"


def test_capped_turn_with_real_reply_shows_the_reply_and_the_cap_notice():
    rendered = render_resolved_turn_text(SESSION_STATE_CAPPED, "the real answer")

    assert "the real answer" in rendered
    assert "hit its budget" in rendered


def test_capped_turn_with_no_transcript_text_falls_back_to_cap_notice_only():
    rendered = render_resolved_turn_text(SESSION_STATE_CAPPED, None)

    assert rendered == (
        "_This conversation hit its budget (turn or cost cap) and has "
        "stopped -- see the session link above for details._"
    )


def test_done_turn_shows_its_transcript_text_unmodified():
    rendered = render_resolved_turn_text(SESSION_STATE_DONE, "the real answer")

    assert rendered == "the real answer"
