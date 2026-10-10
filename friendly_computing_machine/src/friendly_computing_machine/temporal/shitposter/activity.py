"""Activities for the Shitposter generation pipeline (see workflow.py)."""

import asyncio
import datetime
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
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_draft_dal,
    shitposter_snapshot_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobRun,
    ShitposterPrincipalKindEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_draft import (
    ShitposterDraft,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    draft_expiry_hours,
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
PROMPT_LINK_LABEL = "view prompt"
# Slack's section block text limit
_SECTION_TEXT_LIMIT = 3000


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
class SnapshotResult:
    # None when no snapshot exists yet; the persona text is used instead
    snapshot_id: Optional[int] = None
    rendered_text: Optional[str] = None


@dataclass
class SkipParams:
    channel_slack_id: str
    persona_id: int
    reason: str
    context_snapshot_id: Optional[int] = None


@dataclass
class GenerateParams:
    params: ShitpostParams
    persona_text: str
    deadline_seconds: float
    # tells the model to try again after a guardrail block
    attempt: int = 0
    # latest or explicit snapshot text; replaces persona_text when set
    context_text: Optional[str] = None


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
    context_snapshot_id: Optional[int] = None


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
    """Poll until a new assistant reply lands after from_seq.

    A follow-up turn can still read AWAITING_INPUT right after send_turn,
    before the session flips to RUNNING, so a settled state with no new
    reply yet is polled again rather than treated as a failure.
    """
    while True:
        session = client.get_session(session_id)
        if session.state not in (_STATE_AWAITING_INPUT, _STATE_DONE, _STATE_RUNNING):
            raise RuntimeError(f"whagent session ended in state {session.state}")
        if session.state != _STATE_RUNNING:
            reply = client.latest_assistant_message(session_id, from_seq=from_seq)
            if reply is not None:
                return reply[0]
        if time.monotonic() >= deadline:
            if session.state == _STATE_RUNNING:
                raise TimeoutError("whagent generation deadline exceeded")
            raise RuntimeError("whagent session produced no assistant message")
        time.sleep(_POLL_INTERVAL_SECONDS)


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
            # the snapshot stands in for the persona text; persona text is the
            # fallback only before any snapshot exists
            pinned = None
            if params.context_text is None:
                head = params.persona_text
            elif client.supports_pinned_context():
                pinned, head = params.context_text, ""
            else:
                head = params.context_text
            first_turn = (
                f"{head}\n\n{context}{instruction}" if head else f"{context}{instruction}"
            )
            session = client.start_session(
                load_shitposter_agent_id(),
                first_turn=first_turn,
                on_behalf_of=on_behalf_of,
                pinned_context=pinned,
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
async def resolve_snapshot_activity(explicit_snapshot_id: Optional[int] = None) -> SnapshotResult:
    """Explicit snapshot when given, else the default persona's latest one."""
    if explicit_snapshot_id is not None:
        snapshot = shitposter_snapshot_dal.snapshot_by_id(explicit_snapshot_id)
        if snapshot is None:
            raise RuntimeError(f"context snapshot {explicit_snapshot_id} not found")
    else:
        persona = dal.shitposter_dal.get_default_persona()
        if persona is None:
            raise RuntimeError("no shitposter persona is seeded")
        snapshot = shitposter_snapshot_dal.latest_snapshot(persona.id)
    if snapshot is None:
        return SnapshotResult()
    return SnapshotResult(snapshot_id=snapshot.id, rendered_text=snapshot.rendered_text)


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


def _guardrail_reason(text: str) -> Optional[str]:
    """Reason code the text must not post under, or None when every check passes."""
    if not text.strip():
        return "empty"
    if guardrails.find_mention(text):
        return "mention"
    if guardrails.find_member_name(text, _member_names()):
        return "member_name"
    if not guardrails.passes_content_filter(text):
        return "content_filter"
    return None


@activity.defn
async def check_guardrails_activity(text: str) -> GuardrailResult:
    """Mentions, current-member names and the content filter."""
    reason = _guardrail_reason(text)
    return GuardrailResult(reason is None, reason)


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
        context_snapshot_id=params.context_snapshot_id,
    )


@activity.defn
async def record_scheduled_skip_activity(params: SkipParams) -> None:
    """Record a scheduled slot that posted nothing because the writer was unavailable."""
    with SessionManager() as session:
        channel = session.exec(
            select(SlackChannel).where(SlackChannel.slack_id == params.channel_slack_id)
        ).one()
        channel_id = channel.id
    try:
        dal.shitposter_dal.record_scheduled_skip(
            persona_id=params.persona_id,
            slack_channel_id=channel_id,
            reason=params.reason,
            context_snapshot_id=params.context_snapshot_id,
        )
    except Exception:
        logger.exception("scheduled skip record failed: channel=%s", params.channel_slack_id)


def _post_blocks(text: str, whagent_session_id: Optional[str]) -> Optional[list[dict]]:
    """Post text plus a small "view prompt" link to its whagent session.

    Slack has no spoiler or collapsed-section markup, so the link sits in a
    context block under the post. None (plain text post) when no link can be built.
    """
    base_url = get_whagent_client().ui_public_url
    if not base_url or not whagent_session_id or len(text) > _SECTION_TEXT_LIMIT:
        return None
    link = f"{base_url.rstrip('/')}/sessions/{whagent_session_id}"
    return [
        {"type": "section", "text": {"type": "mrkdwn", "text": text}},
        {
            "type": "context",
            "elements": [{"type": "mrkdwn", "text": f"<{link}|{PROMPT_LINK_LABEL}>"}],
        },
    ]


def _post_shitpost(
    channel_slack_id: str,
    text: str,
    whagent_session_id: Optional[str],
    thread_ts: Optional[str] = None,
) -> str:
    """Post to Slack; text stays the bare post so notifications and search omit the link."""
    return slack_post_thread_message(
        channel_slack_id,
        text,
        thread_ts=thread_ts,
        blocks=_post_blocks(text, whagent_session_id),
        unfurl=False,
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
    ts = _post_shitpost(
        params.params.channel_slack_id,
        params.text,
        params.whagent_session_id,
        thread_ts=params.params.thread_ts,
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


@dataclass
class DraftPostParams:
    params: ShitpostParams
    persona: PersonaResult


def _pick_draft(
    session, persona_id: int, now: datetime.datetime, expiry: datetime.timedelta
) -> Optional[ShitposterDraft]:
    """Best-ranked unused, unexpired draft that passes retirement and guardrail checks.

    Failing drafts are discarded with a reason as they are reached.
    """
    for draft in shitposter_draft_dal.unused_drafts(persona_id, session=session):
        if not shitposter_draft_dal.is_unexpired(draft, now, expiry):
            continue
        if shitposter_draft_dal.snapshot_has_retired_item(session, draft.snapshot_id):
            reason = "retired_item"
        else:
            reason = _guardrail_reason(draft.text)
            if reason is not None:
                reason = f"guardrail:{reason}"
        if reason is None:
            return draft
        logger.info("draft %s discarded: %s", draft.id, reason)
        shitposter_draft_dal.discard_draft(session, draft, reason, now)
        session.commit()
    return None


def _post_queued_draft(p: DraftPostParams) -> Optional[ShitpostResult]:
    """Post the best queued draft, or return None so the caller generates on the spot."""
    channel_slack_id = p.params.channel_slack_id
    gate = dal.shitposter_gate(channel_slack_id)
    if not gate.allowed:
        return ShitpostResult(ShitpostOutcome.SKIPPED_GATE, reason=gate.reason)
    now = datetime.datetime.now(datetime.timezone.utc)
    expiry = datetime.timedelta(hours=draft_expiry_hours())
    with SessionManager() as session:
        draft = _pick_draft(session, p.persona.persona_id, now, expiry)
        if draft is None:
            return None
        run = session.get(ShitposterBrainJobRun, draft.brain_job_run_id)
        whagent_session_id = (run.details or {}).get("whagent_session_id") if run else None
        if not whagent_session_id:
            shitposter_draft_dal.discard_draft(session, draft, "missing_writer_session", now)
            session.commit()
            return None
        draft_id, text, snapshot_id = draft.id, draft.text, draft.snapshot_id

    ts = _post_shitpost(channel_slack_id, text, whagent_session_id)
    try:
        _record_draft_post(
            p, draft_id, ts, whagent_session_id, snapshot_id, now
        )
    except Exception:
        # the post is live; a missing record must not leave the draft reusable
        logger.exception("draft post recorded failed: draft=%s ts=%s", draft_id, ts)
        with SessionManager() as session:
            draft = session.get(ShitposterDraft, draft_id)
            shitposter_draft_dal.discard_draft(session, draft, "post_record_failed", now)
            session.commit()
    logger.info("draft posted: channel=%s draft=%s ts=%s", channel_slack_id, draft_id, ts)
    return ShitpostResult(
        ShitpostOutcome.POSTED,
        slack_message_ts=ts,
        whagent_session_id=whagent_session_id,
    )


def _record_draft_post(
    p: DraftPostParams,
    draft_id: int,
    ts: str,
    whagent_session_id: str,
    snapshot_id: int,
    now: datetime.datetime,
) -> None:
    """Insert the post record and mark the draft used in one transaction."""
    iss, sub = get_whagent_client().service_subject()
    with SessionManager() as session:
        channel_id = session.exec(
            select(SlackChannel.id).where(SlackChannel.slack_id == p.params.channel_slack_id)
        ).one()
        draft = session.exec(
            select(ShitposterDraft).where(ShitposterDraft.id == draft_id).with_for_update()
        ).one()
        if draft.used_at is not None:
            raise RuntimeError(f"draft {draft_id} was already used")
        post = dal.shitposter_dal.record_post(
            slack_channel_id=channel_id,
            slack_message_ts=ts,
            persona_id=p.persona.persona_id,
            persona_revision_id=p.persona.persona_revision_id,
            trigger=p.params.trigger,
            principal_iss=iss,
            principal_sub=sub,
            principal_kind=ShitposterPrincipalKindEnum.SERVICE,
            whagent_session_id=whagent_session_id,
            context_snapshot_id=snapshot_id,
            session=session,
            commit=False,
        )
        draft.used_at = now
        draft.used_post_id = post.id
        session.add(draft)
        session.commit()


@activity.defn
async def post_queued_draft_activity(params: DraftPostParams) -> Optional[ShitpostResult]:
    """Scheduled-post path: post a queued draft, or None when none is usable."""
    return await asyncio.to_thread(_post_queued_draft, params)


@activity.defn
async def send_ephemeral_notice_activity(params: NoticeParams) -> None:
    get_slack_web_client().chat_postEphemeral(
        channel=params.channel_slack_id,
        user=params.slack_user_id,
        text=params.text,
        **({"thread_ts": params.thread_ts} if params.thread_ts else {}),
    )


@activity.defn
async def expire_pending_suggestions_activity() -> int:
    """Expire pending persona suggestions past their expires_at; returns the count."""
    return dal.expire_pending_suggestions(datetime.datetime.now(datetime.UTC))


_DECLINED_REASON_LABEL = {
    "off_topic": "off topic",
    "unsafe": "not something Shitposter will post",
    "duplicate": "already covered by the persona",
    "other": "other",
}


def render_suggestion_reply(kind: str, coarse_reason: Optional[str]) -> str:
    """Thread reply for a suggestion outcome; fixed text only, never backers or declined text."""
    if kind == "applied":
        return "This suggestion has been added to my persona."
    if kind == "declined":
        label = _DECLINED_REASON_LABEL.get(coarse_reason or "", "other")
        return f"This suggestion wasn't added to my persona. Reason: {label}."
    return "This suggestion expired before enough people backed it."


@activity.defn
async def drain_suggestion_replies_activity() -> int:
    """Post queued suggestion replies while the kill switch is off; returns how many were posted."""
    if not dal.is_shitposter_enabled():
        return 0
    posted = 0
    for outbox_id, kind, coarse_reason, channel_slack_id, message_ts in (
        dal.list_unposted_suggestion_replies()
    ):
        if not dal.is_shitposter_enabled():
            break
        try:
            slack_post_thread_message(
                channel_slack_id,
                render_suggestion_reply(kind, coarse_reason),
                thread_ts=message_ts,
            )
        except Exception:
            logger.exception("suggestion reply post failed: outbox=%s", outbox_id)
            continue
        dal.mark_suggestion_reply_posted(outbox_id)
        posted += 1
    return posted
