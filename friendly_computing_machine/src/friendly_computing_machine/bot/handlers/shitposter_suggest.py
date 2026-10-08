"""`/shitpost suggest <text>`: a member proposes a persona line as a pending Shitposter post.

Refusals store nothing. Others back a suggestion by reacting; promotion lives elsewhere.
"""

import datetime
import logging

from sqlmodel import select

from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
    slack_post_thread_message,
)
from friendly_computing_machine.src.friendly_computing_machine.db import dal
from friendly_computing_machine.src.friendly_computing_machine.db.dal.shitposter_dal import (
    get_default_persona,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    suggestion_daily_limit,
    suggestion_expiry_days,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
    guardrails,
)

logger = logging.getLogger(__name__)

RATE_WINDOW = datetime.timedelta(hours=24)
USAGE = "Usage: `/shitpost suggest <text>`"
NOT_OPTED_IN = (
    "Persona suggestions are only open in channels where Shitposter is on. "
    "Ask an admin to turn it on here."
)
SILENCED = "Shitposter is silenced right now, so suggestions are closed. Try again later."
NO_PERSONA = "Shitposter has no persona set up yet. Try again later."
POST_FAILED = "Your suggestion could not be posted. Try again."
ACCEPTED = "Your suggestion is pending in this channel. Others can back it by reacting."
REFUSAL_TEXT = {
    "empty": USAGE,
    "mention": "That suggestion can't be posted because it mentions someone.",
    "member_name": "That suggestion can't be posted because it names a member.",
    "content_filter": "That suggestion can't be posted because it contains content Shitposter won't post.",
}


def sweep_expired_suggestions(now: datetime.datetime | None = None) -> int:
    """Mark pending suggestions past their expires_at as expired; returns the count."""
    return dal.expire_pending_suggestions(now or datetime.datetime.now(datetime.UTC))


def _member_names() -> list[str]:
    with SessionManager() as session:
        rows = session.exec(
            select(SlackUser.name).where(SlackUser.is_bot == False)  # noqa: E712
        ).all()
    return [r for r in rows if r]


def _gate_refusal(channel_id: str) -> str | None:
    """Opt-in first, then the kill switch; uncached, so an opt-out lands at once."""
    if not dal.is_channel_opted_in(channel_id):
        return NOT_OPTED_IN
    if not dal.is_shitposter_enabled():
        return SILENCED
    return None


def _as_utc(value: datetime.datetime) -> datetime.datetime:
    return value if value.tzinfo else value.replace(tzinfo=datetime.UTC)


def _rate_refusal(user_id: str, now: datetime.datetime, limit: int) -> str | None:
    """Refuse at the limit, naming when the oldest submission in the window ages out."""
    times = sorted(
        _as_utc(t) for t in dal.list_submitted_at_since(user_id, now - RATE_WINDOW)
    )
    if len(times) < limit:
        return None
    # the (count - limit)th oldest submission frees the next slot
    frees_at = times[len(times) - limit] + RATE_WINDOW
    return (
        f"You've reached {limit} suggestions in the last 24 hours. "
        f"You can submit again after {frees_at:%Y-%m-%d %H:%M} UTC."
    )


def handle_suggest(user_id: str, channel_id: str, text: str, client=None) -> str:
    """Run the suggestion checks; returns the ephemeral reply."""
    now = datetime.datetime.now(datetime.UTC)
    sweep_expired_suggestions(now)

    refusal = _gate_refusal(channel_id)
    if refusal:
        return refusal
    limit = suggestion_daily_limit()
    refusal = _rate_refusal(user_id, now, limit)
    if refusal:
        return refusal
    reason = guardrails.output_refusal(text, _member_names())
    if reason:
        return REFUSAL_TEXT[reason]

    slack_channel = dal.get_slack_channel(slack_channel_slack_id=channel_id)
    persona = get_default_persona()
    if slack_channel is None or persona is None:
        return NO_PERSONA

    # re-check right before posting: a silence or opt-out may have landed during the checks
    refusal = _gate_refusal(channel_id)
    if refusal:
        return refusal

    days = suggestion_expiry_days()
    expires_at = now + datetime.timedelta(days=days)
    body = (
        f"Persona suggestion (pending until {expires_at:%Y-%m-%d} UTC): {text}\n"
        "React to back it. Suggestions with enough backing join the persona."
    )
    try:
        ts = slack_post_thread_message(channel_id, body)
    except Exception:
        logger.exception("persona suggestion post failed: channel=%s", channel_id)
        return POST_FAILED
    try:
        dal.create_suggestion(
            persona_id=persona.id,
            slack_channel_id=slack_channel.id,
            slack_message_ts=ts,
            submitter_slack_user_id=user_id,
            text=text,
            expires_at=expires_at,
            submitted_at=now,
        )
    except Exception:
        # the message is live; a missing record must not read as "not posted"
        logger.exception("persona suggestion posted (ts=%s) but recording failed", ts)
    logger.info("persona suggestion posted: channel=%s ts=%s", channel_id, ts)
    return ACCEPTED
