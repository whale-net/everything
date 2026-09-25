"""Temporal activities for the whagent-net Slack thread relay.

Thin wrappers only -- state machine / turn-queuing logic lives in
workflow.py (SlackThreadAgentWorkflow), matching this repo's existing
activity style (temporal/slack/activity.py).
"""

import logging
from dataclasses import dataclass
from typing import Optional

import grpc
from temporalio import activity

from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
    slack_post_thread_message,
    slack_update_message,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    insert_thread_session,
    update_thread_session_status,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackThreadSessionStatusEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    get_whagent_client,
)

logger = logging.getLogger(__name__)


# ----------------------------------------------------------------------
# whagent-net SessionService activities
# ----------------------------------------------------------------------


@dataclass
class StartWhagentSessionParams:
    agent_id: str
    first_turn: Optional[str] = None
    # (iss, sub) of the Slack user's linked Keycloak identity, or None for a
    # non-delegated start. Optional so in-flight workflow histories carrying
    # the pre-identity payload still deserialize.
    on_behalf_of: Optional[tuple[str, str]] = None


@dataclass
class WhagentSessionResult:
    session_id: str
    state: int


@activity.defn
async def start_whagent_session_activity(
    params: StartWhagentSessionParams,
) -> WhagentSessionResult:
    client = get_whagent_client()
    try:
        session = client.start_session(
            params.agent_id,
            first_turn=params.first_turn,
            on_behalf_of=params.on_behalf_of,
        )
    except grpc.RpcError as e:
        if e.code() == grpc.StatusCode.PERMISSION_DENIED:
            logger.error(
                "whagent-net denied StartSession -- a delegated start needs "
                "fcm's client_id in whagent-net's on_behalf_of allowlist "
                "(agent_id=%s on_behalf_of=%s)",
                params.agent_id,
                params.on_behalf_of,
            )
        raise
    return WhagentSessionResult(session_id=session.session_id, state=session.state)


@dataclass
class SendWhagentTurnParams:
    session_id: str
    input: str


@activity.defn
async def send_whagent_turn_activity(
    params: SendWhagentTurnParams,
) -> WhagentSessionResult:
    client = get_whagent_client()
    session = client.send_turn(params.session_id, params.input)
    return WhagentSessionResult(session_id=session.session_id, state=session.state)


@dataclass
class WhagentSessionStatus:
    session_id: str
    state: int
    cap_kind: Optional[int] = None
    error_category: Optional[int] = None
    error_detail: Optional[str] = None


@activity.defn
async def get_whagent_session_activity(session_id: str) -> WhagentSessionStatus:
    client = get_whagent_client()
    session = client.get_session(session_id)
    return WhagentSessionStatus(
        session_id=session.session_id,
        state=session.state,
        cap_kind=session.cap_kind if session.HasField("cap_kind") else None,
        error_category=(
            session.error_category if session.HasField("error_category") else None
        ),
        error_detail=session.error_detail if session.HasField("error_detail") else None,
    )


@dataclass
class ReadWhagentTranscriptParams:
    session_id: str
    from_seq: int = 0


@dataclass
class WhagentTranscriptResult:
    text: str
    seq: int


@activity.defn
async def read_whagent_transcript_activity(
    params: ReadWhagentTranscriptParams,
) -> Optional[WhagentTranscriptResult]:
    """Return the latest assistant_message event at or after from_seq, if any."""
    client = get_whagent_client()
    result = client.latest_assistant_message(params.session_id, from_seq=params.from_seq)
    if result is None:
        return None
    text, seq = result
    return WhagentTranscriptResult(text=text, seq=seq)


# ----------------------------------------------------------------------
# DB activities
# ----------------------------------------------------------------------


@dataclass
class InsertThreadSessionParams:
    slack_channel_id: int
    thread_ts: str
    whagent_session_id: str


@activity.defn
async def insert_thread_session_activity(params: InsertThreadSessionParams) -> int:
    row = insert_thread_session(
        slack_channel_id=params.slack_channel_id,
        thread_ts=params.thread_ts,
        whagent_session_id=params.whagent_session_id,
    )
    return row.id


@dataclass
class UpdateThreadSessionStatusParams:
    thread_session_id: int
    status: SlackThreadSessionStatusEnum


@activity.defn
async def update_thread_session_status_activity(
    params: UpdateThreadSessionStatusParams,
) -> None:
    update_thread_session_status(params.thread_session_id, params.status)


# ----------------------------------------------------------------------
# Slack activities
# ----------------------------------------------------------------------


@dataclass
class PostSlackThreadMessageParams:
    channel_id: str
    text: str
    thread_ts: Optional[str] = None


@activity.defn
async def post_slack_thread_message_activity(params: PostSlackThreadMessageParams) -> str:
    return slack_post_thread_message(
        params.channel_id, params.text, thread_ts=params.thread_ts
    )


@dataclass
class UpdateSlackMessageParams:
    channel_id: str
    ts: str
    text: str
    # Render text as standard Markdown (agent replies) rather than Slack mrkdwn.
    # Defaulted so in-flight workflow histories still deserialize.
    markdown: bool = False


# Slack caps the cumulative text of all markdown blocks in one payload.
SLACK_MARKDOWN_BLOCK_MAX_CHARS = 12000


def markdown_blocks(text: str) -> Optional[list[dict]]:
    """Wrap text in a Slack markdown block, which renders standard Markdown.

    Returns None when text exceeds Slack's markdown block limit, so the
    caller falls back to a plain text message instead of a rejected payload.
    """
    if not text or len(text) > SLACK_MARKDOWN_BLOCK_MAX_CHARS:
        return None
    return [{"type": "markdown", "text": text}]


@activity.defn
async def update_slack_message_activity(params: UpdateSlackMessageParams) -> str:
    blocks = markdown_blocks(params.text) if params.markdown else None
    if params.markdown and blocks is None:
        logger.warning(
            "agent reply too long for a Slack markdown block (%d chars), "
            "posting as plain text",
            len(params.text),
        )
    return slack_update_message(params.channel_id, params.ts, params.text, blocks=blocks)
