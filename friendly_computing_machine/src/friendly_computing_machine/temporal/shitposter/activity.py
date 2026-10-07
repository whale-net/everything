"""Activities for the Shitposter generation pipeline (see workflow.py)."""

import asyncio
import logging
import time
from dataclasses import dataclass
from typing import Optional

from sqlmodel import select
from temporalio import activity

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    get_slack_web_client,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
    slack_post_thread_message,
)
from friendly_computing_machine.src.friendly_computing_machine.db import dal
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterPrincipalKindEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
    guardrails,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
    ShitpostOutcome,
    ShitpostParams,
    ShitpostResult,
    load_shitposter_agent_id,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    get_whagent_client,
)

logger = logging.getLogger(__name__)

# whagent_net SessionState values (session.proto)
_STATE_RUNNING = 1
_STATE_AWAITING_INPUT = 2
_STATE_DONE = 3
_POLL_INTERVAL_SECONDS = 1.0

SCHEDULED_INSTRUCTION = "Write one original shitpost for the channel. Reply with only the post text."
SUMMON_INSTRUCTION = "Write one original shitpost on demand. Reply with only the post text."
RIFF_INSTRUCTION = "Reply in the thread with a short riff. Reply with only the text."
RETRY_SUFFIX = " Your previous attempt was rejected; take a different angle and do not name or mention any person."


@dataclass
class GateActivityResult:
    allowed: bool
    reason: Optional[str] = None


@dataclass
class PersonaResult:
    persona_id: int
    persona_revision_id: int
    persona_text: str


@dataclass
class GenerateParams:
    params: ShitpostParams
    persona_text: str
    deadline_seconds: float
    # tells the model to try again after a guardrail block
    attempt: int = 0


@dataclass
class GenerateResult:
    ok: bool
    text: str = ""
    whagent_session_id: Optional[str] = None
    error: Optional[str] = None
    timed_out: bool = False


@dataclass
class GuardrailResult:
    allowed: bool
    reason: Optional[str] = None


@dataclass
class PostParams:
    params: ShitpostParams
    text: str
    persona: PersonaResult
    whagent_session_id: str


@dataclass
class NoticeParams:
    channel_slack_id: str
    slack_user_id: str
    text: str
    thread_ts: Optional[str] = None


def _instruction(params: ShitpostParams, attempt: int) -> str:
    base = {
        "scheduled": SCHEDULED_INSTRUCTION,
        "summon": SUMMON_INSTRUCTION,
        "riff": RIFF_INSTRUCTION,
    }[params.trigger]
    if params.topic:
        base += f" Topic: {params.topic}"
    if attempt > 0:
        base += RETRY_SUFFIX
    return base


def _wait_for_reply(client, session_id: str, from_seq: int, deadline: float) -> str:
    """Poll until the session leaves RUNNING, then return its newest reply."""
    while True:
        session = client.get_session(session_id)
        if session.state in (_STATE_AWAITING_INPUT, _STATE_DONE):
            break
        if session.state != _STATE_RUNNING:
            raise RuntimeError(f"whagent session ended in state {session.state}")
        if time.monotonic() >= deadline:
            raise TimeoutError("whagent generation deadline exceeded")
        time.sleep(_POLL_INTERVAL_SECONDS)
    reply = client.latest_assistant_message(session_id, from_seq=from_seq)
    if reply is None:
        raise RuntimeError("whagent session produced no assistant message")
    return reply[0]


def _generate_blocking(params: GenerateParams) -> GenerateResult:
    client = get_whagent_client()
    deadline = time.monotonic() + params.deadline_seconds
    p = params.params
    instruction = _instruction(p, params.attempt)
    try:
        if p.whagent_session_id:
            session_id = p.whagent_session_id
            _, from_seq = client.read_transcript(session_id, from_seq=0)
            client.send_turn(session_id, instruction)
        else:
            # Persona text is the session's first turn; the agent definition
            # holds only a base prompt. Human principals run on_behalf_of;
            # the service subject sends none.
            on_behalf_of = (
                (p.principal_iss, p.principal_sub)
                if p.principal_iss and p.principal_sub
                else None
            )
            context = (
                f"Thread so far:\n{p.thread_context}\n\n" if p.thread_context else ""
            )
            session = client.start_session(
                load_shitposter_agent_id(),
                first_turn=f"{params.persona_text}\n\n{context}{instruction}",
                on_behalf_of=on_behalf_of,
            )
            session_id = session.session_id
            from_seq = 0
        text = _wait_for_reply(client, session_id, from_seq, deadline)
    except TimeoutError as e:
        return GenerateResult(ok=False, error=str(e), timed_out=True)
    except Exception as e:
        logger.error("shitpost generation failed: %s", e)
        return GenerateResult(ok=False, error=str(e))
    return GenerateResult(ok=True, text=text.strip(), whagent_session_id=session_id)


@activity.defn
async def shitposter_gate_activity(channel_slack_id: str) -> GateActivityResult:
    """Uncached gate read."""
    result = dal.shitposter_gate(channel_slack_id)
    return GateActivityResult(allowed=result.allowed, reason=result.reason)


@activity.defn
async def resolve_persona_activity(_: None = None) -> PersonaResult:
    """Current revision of the default persona."""
    persona = dal.shitposter_dal.get_default_persona()
    if persona is None:
        raise RuntimeError("no shitposter persona is seeded")
    revision = dal.shitposter_dal.get_current_persona_revision(persona.id)
    if revision is None:
        raise RuntimeError(f"persona {persona.id} has no current revision")
    return PersonaResult(
        persona_id=persona.id,
        persona_revision_id=revision.id,
        persona_text=revision.persona_text,
    )


@activity.defn
async def generate_shitpost_activity(params: GenerateParams) -> GenerateResult:
    """Start (or continue) a whagent session; persona text in the first turn."""
    return await asyncio.to_thread(_generate_blocking, params)


def _member_names() -> list[str]:
    with SessionManager() as session:
        rows = session.exec(
            select(SlackUser.name).where(SlackUser.is_bot == False)  # noqa: E712
        ).all()
    return [r for r in rows if r]


@activity.defn
async def check_guardrails_activity(text: str) -> GuardrailResult:
    """Mentions, current-member names and the content filter."""
    if not text.strip():
        return GuardrailResult(False, "empty")
    if guardrails.find_mention(text):
        return GuardrailResult(False, "mention")
    if guardrails.find_member_name(text, _member_names()):
        return GuardrailResult(False, "member_name")
    if not guardrails.passes_content_filter(text):
        return GuardrailResult(False, "content_filter")
    return GuardrailResult(True)


def _record(params: PostParams, ts: str) -> None:
    p = params.params
    if p.principal_iss and p.principal_sub:
        iss, sub, kind = p.principal_iss, p.principal_sub, ShitposterPrincipalKindEnum.HUMAN
    else:
        iss, sub = get_whagent_client().service_subject()
        kind = ShitposterPrincipalKindEnum.SERVICE
    with SessionManager() as session:
        channel = session.exec(
            select(SlackChannel).where(SlackChannel.slack_id == p.channel_slack_id)
        ).one()
        channel_id = channel.id
    dal.shitposter_dal.record_post(
        slack_channel_id=channel_id,
        slack_message_ts=ts,
        persona_id=params.persona.persona_id,
        persona_revision_id=params.persona.persona_revision_id,
        trigger=p.trigger,
        principal_iss=iss,
        principal_sub=sub,
        principal_kind=kind,
        whagent_session_id=params.whagent_session_id,
        thread_ts=p.thread_ts,
        thread_owner_slack_user_id=(
            p.thread_owner_slack_user_id or p.notice_slack_user_id
            if p.trigger == "summon"
            else p.thread_owner_slack_user_id
        ),
        parent_post_id=p.parent_post_id,
    )


@activity.defn
async def post_and_record_shitpost_activity(params: PostParams) -> ShitpostResult:
    """Re-check the gate, post to Slack, record the post. Never retried.

    Run with maximum_attempts=1: Slack has no idempotency key, so a retry
    after a post that landed would double-post.
    """
    gate = dal.shitposter_gate(params.params.channel_slack_id)
    if not gate.allowed:
        return ShitpostResult(ShitpostOutcome.SKIPPED_GATE, reason=gate.reason)
    ts = slack_post_thread_message(
        params.params.channel_slack_id, params.text, thread_ts=params.params.thread_ts
    )
    try:
        _record(params, ts)
    except Exception:
        # the post is live; a missing record must not read as "not posted"
        logger.exception("shitpost posted (ts=%s) but recording failed", ts)
    logger.info(
        "shitpost posted: channel=%s trigger=%s ts=%s",
        params.params.channel_slack_id,
        params.params.trigger,
        ts,
    )
    return ShitpostResult(
        ShitpostOutcome.POSTED,
        slack_message_ts=ts,
        whagent_session_id=params.whagent_session_id,
    )


@activity.defn
async def send_ephemeral_notice_activity(params: NoticeParams) -> None:
    get_slack_web_client().chat_postEphemeral(
        channel=params.channel_slack_id,
        user=params.slack_user_id,
        text=params.text,
        **({"thread_ts": params.thread_ts} if params.thread_ts else {}),
    )
