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

        while True:
            self._queue.turn_in_flight = True
            final_text = await self._resolve_turn(session_id)
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

    async def _resolve_turn(self, session_id: str) -> str:
        """Poll until the turn leaves RUNNING, then render its result text."""
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
                "link above for its current status._"
            )

        if status.state == SESSION_STATE_CAPPED:
            return (
                "This conversation hit its budget (turn or cost cap) and has "
                "stopped -- see the session link above for details."
            )
        if status.state == SESSION_STATE_FAILED:
            detail = f" ({status.error_detail})" if status.error_detail else ""
            return f"This turn failed{detail} -- see the session link above for details."

        text = await workflow.execute_activity(
            read_whagent_transcript_activity,
            ReadWhagentTranscriptParams(session_id=session_id, from_seq=0),
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )
        return text or "_(no response text found -- see the session link above)_"
