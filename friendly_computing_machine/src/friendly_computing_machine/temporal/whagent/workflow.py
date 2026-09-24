"""Per-Slack-thread relay to a whagent-net agent session.

One SlackThreadAgentWorkflow instance runs for the lifetime of one Slack
thread's conversation with an agent: it starts the whagent-net session,
posts/updates a single Slack message per turn with the agent's response,
and queues any Slack replies that arrive while a turn is still running so
they're sent as one combined next turn once the current one resolves --
never as an immediate second SendTurn.
"""

import asyncio
import logging
from dataclasses import dataclass
from datetime import timedelta
from typing import Optional

from temporalio import workflow

from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackThreadSessionStatusEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.activity import (
    InsertThreadSessionParams,
    PostSlackThreadMessageParams,
    ReadWhagentTranscriptParams,
    SendWhagentTurnParams,
    StartWhagentSessionParams,
    UpdateSlackMessageParams,
    UpdateThreadSessionStatusParams,
    get_whagent_session_activity,
    insert_thread_session_activity,
    post_slack_thread_message_activity,
    read_whagent_transcript_activity,
    send_whagent_turn_activity,
    start_whagent_session_activity,
    update_slack_message_activity,
    update_thread_session_status_activity,
)

logger = logging.getLogger(__name__)

# Mirrors whagent_net/protos/session.proto's SessionState enum values --
# kept as plain ints here (rather than importing the generated proto enum)
# so the workflow module's only imports are this repo's own dataclasses,
# deterministic and safe inside the Temporal sandbox either way.
SESSION_STATE_RUNNING = 1
SESSION_STATE_AWAITING_INPUT = 2
SESSION_STATE_DONE = 3
SESSION_STATE_STOPPED = 4
SESSION_STATE_FAILED = 5
SESSION_STATE_CAPPED = 6

POLL_INTERVAL = timedelta(seconds=2)
TURN_TIMEOUT = timedelta(minutes=5)
IDLE_TIMEOUT = timedelta(minutes=30)
ACTIVITY_TIMEOUT = timedelta(seconds=20)
START_ACTIVITY_TIMEOUT = timedelta(seconds=60)

THINKING_TEXT = "_Thinking..._"


def workflow_id_for_thread(app_env: str, channel_id: str, thread_ts: str) -> str:
    """The deterministic workflow id for a Slack thread's relay.

    Shared by the app_mention handler (which starts the workflow) and the
    follow-up message handler (which needs the same id to signal it) so
    neither has to look the other up -- SlackThreadSession is still the
    fast "is this thread active at all" check before attempting to signal
    (a signal to a nonexistent/completed workflow id is slower and
    messier to handle than a DB row check).
    """
    return f"fcm-{app_env}-whagent-thread-{channel_id}-{thread_ts}"


def render_resolved_turn_text(state: int, text: Optional[str]) -> str:
    """Render the Slack message body for a turn that left RUNNING cleanly
    (not FAILED, not timed out) -- DONE, AWAITING_INPUT, or CAPPED.

    A capped turn still runs to completion and commits its own transcript
    event before the session flips to CAPPED (whagent_net/worker/caps.go) --
    only a cap that trips mid-tool-loop leaves no final assistant message,
    so `text` can still be empty there. Either way the real reply, when one
    exists, must win over the generic cap notice rather than being replaced
    by it. Kept Temporal-free (like TurnQueue above) so this branching is
    unit testable without a Temporal test environment.
    """
    if state == SESSION_STATE_CAPPED:
        cap_notice = (
            "_This conversation hit its budget (turn or cost cap) and has "
            "stopped -- see the session link above for details._"
        )
        return f"{text}\n\n{cap_notice}" if text else cap_notice
    return text or "_(no response text found -- see the session link above)_"


class TurnQueue:
    """The turn-queuing state machine, kept deliberately Temporal-free.

    A message that arrives while a turn is in flight must not trigger an
    immediate new turn -- it's buffered here until the current turn
    resolves, then every buffered message is drained as one combined next
    turn. Factored out of SlackThreadAgentWorkflow so this logic (the
    actual thing "messages get queued for the next turn" depends on) can
    be unit tested without a Temporal test environment.
    """

    def __init__(self):
        self._pending: list[str] = []
        self.turn_in_flight: bool = False

    def queue_message(self, text: str) -> None:
        self._pending.append(text)

    def has_pending(self) -> bool:
        return len(self._pending) > 0

    def drain(self) -> Optional[str]:
        """Pop every queued message, joined into one combined turn input.

        Returns None if nothing was queued.
        """
        if not self._pending:
            return None
        combined = "\n".join(self._pending)
        self._pending = []
        return combined


@dataclass
class SlackThreadAgentWorkflowParams:
    channel_slack_id: str
    channel_db_id: int
    thread_ts: str
    agent_id: str
    first_message: str
    slack_user_id: str
    whagent_ui_public_url: str


@workflow.defn
class SlackThreadAgentWorkflow:
    def __init__(self) -> None:
        self._queue = TurnQueue()
        # Extension point for a future stop_session signal -- nothing
        # sets this today, but the idle-wait below already treats it the
        # same as "a message arrived" so adding one later needs no other
        # change to the loop.
        self._stop_requested: bool = False

    @workflow.signal
    def queue_message(self, text: str) -> None:
        self._queue.queue_message(text)

    @workflow.run
    async def run(self, params: SlackThreadAgentWorkflowParams) -> str:
        start_result = await workflow.execute_activity(
            start_whagent_session_activity,
            StartWhagentSessionParams(
                agent_id=params.agent_id, first_turn=params.first_message
            ),
            start_to_close_timeout=START_ACTIVITY_TIMEOUT,
        )
        session_id = start_result.session_id

        thread_session_id = await workflow.execute_activity(
            insert_thread_session_activity,
            InsertThreadSessionParams(
                slack_channel_id=params.channel_db_id,
                thread_ts=params.thread_ts,
                whagent_session_id=session_id,
            ),
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )

        # The link gets its own message so per-turn placeholder updates never
        # overwrite it.
        session_link = f"{params.whagent_ui_public_url.rstrip('/')}/sessions/{session_id}"
        await workflow.execute_activity(
            post_slack_thread_message_activity,
            PostSlackThreadMessageParams(
                channel_id=params.channel_slack_id,
                thread_ts=params.thread_ts,
                text=f"Started a whagent-net session: {session_link}",
            ),
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )
        current_ts = await workflow.execute_activity(
            post_slack_thread_message_activity,
            PostSlackThreadMessageParams(
                channel_id=params.channel_slack_id,
                thread_ts=params.thread_ts,
                text=THINKING_TEXT,
            ),
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )

        # Bounds each turn's transcript read to events committed at or after
        # this position -- otherwise a turn that ends without committing its
        # own assistant_message (the mid-tool-loop cap trip) would be
        # rendered using a *previous* turn's stale reply (both reads default
        # to from_seq=0 and "latest assistant_message in the whole session"
        # looks the same either way unless this boundary moves each turn).
        transcript_seq = 0

        while True:
            self._queue.turn_in_flight = True
            final_text, transcript_seq = await self._resolve_turn(
                session_id, transcript_seq
            )
            await workflow.execute_activity(
                update_slack_message_activity,
                UpdateSlackMessageParams(
                    channel_id=params.channel_slack_id, ts=current_ts, text=final_text
                ),
                start_to_close_timeout=ACTIVITY_TIMEOUT,
            )
            self._queue.turn_in_flight = False

            if not self._queue.has_pending() and not self._stop_requested:
                try:
                    await workflow.wait_condition(
                        lambda: self._queue.has_pending() or self._stop_requested,
                        timeout=IDLE_TIMEOUT,
                    )
                except asyncio.TimeoutError:
                    logger.info(
                        "whagent thread relay idle for %s, closing session=%s",
                        IDLE_TIMEOUT,
                        session_id,
                    )

            combined_input = self._queue.drain()
            if self._stop_requested or combined_input is None:
                await workflow.execute_activity(
                    update_thread_session_status_activity,
                    UpdateThreadSessionStatusParams(
                        thread_session_id=thread_session_id,
                        status=SlackThreadSessionStatusEnum.CLOSED,
                    ),
                    start_to_close_timeout=ACTIVITY_TIMEOUT,
                )
                return session_id

            current_ts = await workflow.execute_activity(
                post_slack_thread_message_activity,
                PostSlackThreadMessageParams(
                    channel_id=params.channel_slack_id,
                    thread_ts=params.thread_ts,
                    text=THINKING_TEXT,
                ),
                start_to_close_timeout=ACTIVITY_TIMEOUT,
            )

            self._queue.turn_in_flight = True
            await workflow.execute_activity(
                send_whagent_turn_activity,
                SendWhagentTurnParams(session_id=session_id, input=combined_input),
                start_to_close_timeout=ACTIVITY_TIMEOUT,
            )

    async def _resolve_turn(
        self, session_id: str, transcript_seq: int
    ) -> tuple[str, int]:
        """Poll until the turn leaves RUNNING, then render its result text.

        Returns the rendered text plus the transcript position to resume
        from on the *next* turn (unchanged from transcript_seq when this
        turn didn't advance the transcript, e.g. the timeout/failed paths).
        """
        elapsed = timedelta()
        status = None
        while elapsed < TURN_TIMEOUT:
            status = await workflow.execute_activity(
                get_whagent_session_activity,
                session_id,
                start_to_close_timeout=ACTIVITY_TIMEOUT,
            )
            if status.state != SESSION_STATE_RUNNING:
                break
            await workflow.sleep(POLL_INTERVAL)
            elapsed += POLL_INTERVAL
        else:
            return (
                "_This turn timed out waiting for whagent-net -- check the session "
                "link above for its current status._",
                transcript_seq,
            )

        if status.state == SESSION_STATE_FAILED:
            detail = f" ({status.error_detail})" if status.error_detail else ""
            return (
                f"This turn failed{detail} -- see the session link above for details.",
                transcript_seq,
            )

        if status.state == SESSION_STATE_CAPPED and not workflow.patched(
            "whagent-capped-turn-shows-reply"
        ):
            # A workflow execution already open (still in the idle-wait
            # below) when this patch deploys must keep replaying its old
            # history exactly -- that history never scheduled the
            # read_whagent_transcript_activity call below for a capped
            # turn, so scheduling it now on replay would be a
            # nondeterminism error (whagent_net/worker/caps.go's own
            # workflow.GetVersion gate is the Go-side precedent for this).
            return (
                "This conversation hit its budget (turn or cost cap) and has "
                "stopped -- see the session link above for details.",
                transcript_seq,
            )

        read = await workflow.execute_activity(
            read_whagent_transcript_activity,
            ReadWhagentTranscriptParams(session_id=session_id, from_seq=transcript_seq),
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )
        return render_resolved_turn_text(status.state, read.text), read.next_from_seq
