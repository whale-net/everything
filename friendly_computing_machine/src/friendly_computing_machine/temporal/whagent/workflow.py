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
    WhagentTranscriptResult,
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


def resolve_turn_outcome(
    state: int,
    error_detail: Optional[str],
    since_seq: int,
    transcript_result: Optional[WhagentTranscriptResult],
) -> Optional[tuple[str, int]]:
    """Decide whether one poll's (session state, transcript read) resolves the turn.

    Returns (text, next_since_seq) once resolved, or None to keep polling.
    Kept Temporal-free (like TurnQueue below) so the actual bug this
    guards against -- a poll that lands between SendTurn returning and the
    session genuinely leaving RUNNING, or an agent's non-final
    assistant_message during a still-RUNNING turn -- can be unit tested
    without a Temporal test environment.

    CAPPED/FAILED are terminal regardless of RUNNING/transcript state.
    Otherwise a poll only resolves once the session has left RUNNING
    *and* transcript_result is a genuinely new assistant_message (its
    caller only passes one when since_seq has advanced past it) --
    RUNNING with a message means an intermediate message from a
    multi-message turn, and not-RUNNING with no message means a stale
    read of the *previous* turn's terminal state, caught before SendTurn's
    new turn has been observed as started.

    A capped turn still runs to completion and commits its own transcript
    event before the session flips to CAPPED (whagent_net/worker/caps.go)
    -- only a cap that trips mid-tool-loop leaves no final assistant
    message, so transcript_result can still be None here. Either way the
    real reply, when one exists, must win over the generic cap notice
    rather than being replaced by it -- so it's shown with the notice
    appended, not discarded.
    """
    if state == SESSION_STATE_CAPPED:
        cap_notice = (
            "_This conversation hit its budget (turn or cost cap) and has "
            "stopped -- see the session link above for details._"
        )
        if transcript_result is not None:
            return (
                f"{transcript_result.text}\n\n{cap_notice}",
                transcript_result.seq + 1,
            )
        return (cap_notice, since_seq)
    if state == SESSION_STATE_FAILED:
        detail = f" ({error_detail})" if error_detail else ""
        return (
            f"This turn failed{detail} -- see the session link above for details.",
            since_seq,
        )
    if state != SESSION_STATE_RUNNING and transcript_result is not None:
        return transcript_result.text, transcript_result.seq + 1
    return None


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
    # The mentioning Slack user's linked Keycloak (iss, sub), or None when
    # the user has no stored mapping. Both default to None so in-flight
    # workflow histories recorded before identity linking still deserialize.
    on_behalf_of_iss: Optional[str] = None
    on_behalf_of_sub: Optional[str] = None


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
        on_behalf_of = None
        if params.on_behalf_of_iss and params.on_behalf_of_sub:
            on_behalf_of = (params.on_behalf_of_iss, params.on_behalf_of_sub)

        try:
            start_result = await workflow.execute_activity(
                start_whagent_session_activity,
                StartWhagentSessionParams(
                    agent_id=params.agent_id,
                    first_turn=params.first_message,
                    on_behalf_of=on_behalf_of,
                ),
                start_to_close_timeout=START_ACTIVITY_TIMEOUT,
            )
        except Exception:
            # A delegated start can be refused (e.g. fcm's client_id is not
            # yet in whagent-net's on_behalf_of allowlist). Reply in-thread so
            # the user isn't left with silence; the activity's ERROR log has
            # the underlying reason.
            logger.exception("failed to start whagent-net session")
            await workflow.execute_activity(
                post_slack_thread_message_activity,
                PostSlackThreadMessageParams(
                    channel_id=params.channel_slack_id,
                    thread_ts=params.thread_ts,
                    text=(
                        "_Couldn't start a whagent-net session for this "
                        "request. If you are a linked Slack user, this may be "
                        "a permissions issue -- check the bot logs._"
                    ),
                ),
                start_to_close_timeout=ACTIVITY_TIMEOUT,
            )
            raise
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

        since_seq = 0
        while True:
            self._queue.turn_in_flight = True
            final_text, since_seq = await self._resolve_turn(session_id, since_seq)
            await workflow.execute_activity(
                update_slack_message_activity,
                UpdateSlackMessageParams(
                    channel_id=params.channel_slack_id,
                    ts=current_ts,
                    text=final_text,
                    markdown=True,
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

    async def _resolve_turn(self, session_id: str, since_seq: int) -> tuple[str, int]:
        """Poll until the turn genuinely resolves, then render its result text.

        Returns the reply text and the transcript seq to use as next
        turn's watermark. See resolve_turn_outcome for what "genuinely
        resolves" requires -- transcript is only read once the session
        has left RUNNING, both to avoid an extra call per poll while a
        turn is still in flight and because a message seen while still
        RUNNING would just be an intermediate one for a multi-message
        turn.
        """
        elapsed = timedelta()
        while elapsed < TURN_TIMEOUT:
            status = await workflow.execute_activity(
                get_whagent_session_activity,
                session_id,
                start_to_close_timeout=ACTIVITY_TIMEOUT,
            )

            transcript_result: Optional[WhagentTranscriptResult] = None
            if status.state != SESSION_STATE_RUNNING:
                transcript_result = await workflow.execute_activity(
                    read_whagent_transcript_activity,
                    ReadWhagentTranscriptParams(session_id=session_id, from_seq=since_seq),
                    start_to_close_timeout=ACTIVITY_TIMEOUT,
                )

            outcome = resolve_turn_outcome(
                status.state, status.error_detail, since_seq, transcript_result
            )
            if outcome is not None:
                return outcome

            await workflow.sleep(POLL_INTERVAL)
            elapsed += POLL_INTERVAL

        return (
            "_This turn timed out waiting for whagent-net -- check the session "
            "link above for its current status._",
            since_seq,
        )
