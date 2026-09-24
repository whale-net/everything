"""Unit tests for the whagent thread relay's turn-queuing and turn-resolution logic.

This repo has no existing WorkflowEnvironment/time-skipping Temporal test
pattern anywhere (checked friendly_computing_machine/tests and the rest of
the repo), so per AGENTS.md's guidance this exercises the queuing and
resolution logic directly rather than inventing a new Temporal test
harness: TurnQueue and resolve_turn_outcome (temporal/whagent/workflow.py)
are plain, Temporal-free functions/classes the workflow delegates to for
exactly this reason.
"""

from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.activity import (
    WhagentTranscriptResult,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.workflow import (
    SESSION_STATE_AWAITING_INPUT,
    SESSION_STATE_CAPPED,
    SESSION_STATE_FAILED,
    SESSION_STATE_RUNNING,
    TurnQueue,
    resolve_turn_outcome,
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


# resolve_turn_outcome -- regression coverage for the stale-reply bug: a
# poll landing between SendTurn returning and the session genuinely
# leaving RUNNING must not hand back the *previous* turn's already-shown
# answer, and a still-RUNNING turn's intermediate assistant_message must
# not be mistaken for the final one.


def test_stale_not_running_with_no_new_message_keeps_polling():
    # The exact race from the bug report: GetSession still reports the
    # previous turn's terminal state right after SendTurn returns, and no
    # new transcript event has landed yet.
    outcome = resolve_turn_outcome(
        SESSION_STATE_AWAITING_INPUT,
        None,
        since_seq=5,
        transcript_result=None,
    )

    assert outcome is None


def test_running_with_a_message_keeps_polling_not_final_yet():
    # A multi-message turn (e.g. "let me check..." then a tool call then
    # the real answer) must not be treated as resolved just because a
    # message showed up while the session is still RUNNING.
    outcome = resolve_turn_outcome(
        SESSION_STATE_RUNNING,
        None,
        since_seq=5,
        transcript_result=WhagentTranscriptResult(text="let me check...", seq=6),
    )

    assert outcome is None


def test_not_running_with_a_new_message_resolves_with_its_text_and_next_seq():
    outcome = resolve_turn_outcome(
        SESSION_STATE_AWAITING_INPUT,
        None,
        since_seq=5,
        transcript_result=WhagentTranscriptResult(text="the real answer", seq=8),
    )

    assert outcome == ("the real answer", 9)


def test_capped_with_no_transcript_text_falls_back_to_cap_notice_only():
    # The mid-tool-loop cap trip: the turn ends without committing its own
    # final assistant_message, so there's genuinely nothing else to show.
    outcome = resolve_turn_outcome(
        SESSION_STATE_CAPPED, None, since_seq=5, transcript_result=None
    )

    assert outcome is not None
    text, next_seq = outcome
    assert "budget" in text
    assert next_seq == 5


def test_capped_with_a_real_reply_shows_the_reply_and_the_cap_notice():
    # The common cap trip path: the turn that hits the cap still runs to
    # completion and commits its own transcript event first
    # (whagent_net/worker/caps.go) -- that real reply must win over the
    # generic cap notice rather than being discarded by it.
    outcome = resolve_turn_outcome(
        SESSION_STATE_CAPPED,
        None,
        since_seq=5,
        transcript_result=WhagentTranscriptResult(text="the real answer", seq=8),
    )

    assert outcome is not None
    text, next_seq = outcome
    assert "the real answer" in text
    assert "budget" in text
    assert next_seq == 9


def test_failed_resolves_immediately_and_includes_error_detail():
    outcome = resolve_turn_outcome(
        SESSION_STATE_FAILED, "boom", since_seq=5, transcript_result=None
    )

    assert outcome is not None
    text, next_seq = outcome
    assert "boom" in text
    assert next_seq == 5
