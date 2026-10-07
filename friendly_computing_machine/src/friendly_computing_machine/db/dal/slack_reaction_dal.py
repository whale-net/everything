"""Slack reaction capture: idempotent add, remove-by-stamp, active listing."""

import datetime
import logging
import re
from typing import Optional

from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackMessage,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack_reaction import (
    SlackReaction,
)
from friendly_computing_machine.src.friendly_computing_machine.util import (
    ts_to_datetime,
)

logger = logging.getLogger(__name__)

_SKIN_TONE = re.compile(r"::skin-tone-\d+$")


def normalize_emoji(reaction: str) -> str:
    """Strip the skin-tone suffix so tone variants count as one emoji."""
    return _SKIN_TONE.sub("", reaction)


def _stored_message_id(
    session: Session, channel_slack_id: str, message_ts: str
) -> Optional[int]:
    try:
        ts = ts_to_datetime(message_ts)
    except (TypeError, ValueError):
        return None
    return session.exec(
        select(SlackMessage.id)
        .where(SlackMessage.slack_channel_slack_id == channel_slack_id)
        .where(SlackMessage.ts == ts)
    ).first()


def _active(
    session: Session, channel: str, message_ts: str, user: str, emoji: str
) -> Optional[SlackReaction]:
    return session.exec(
        select(SlackReaction)
        .where(SlackReaction.slack_channel_slack_id == channel)
        .where(SlackReaction.message_ts == message_ts)
        .where(SlackReaction.slack_user_slack_id == user)
        .where(SlackReaction.emoji == emoji)
        .where(SlackReaction.removed_at.is_(None))  # type: ignore[union-attr]
    ).first()


def add_reaction(
    slack_channel_slack_id: str,
    message_ts: str,
    slack_user_slack_id: str,
    emoji: str,
    added_at: Optional[datetime.datetime] = None,
    added_by_backfill: bool = False,
    is_bot: Optional[bool] = None,
    session: Optional[Session] = None,
) -> Optional[SlackReaction]:
    """Open an active row; returns None when one is already active (redelivery)."""
    emoji = normalize_emoji(emoji)
    with SessionManager(session) as session:
        if _active(
            session, slack_channel_slack_id, message_ts, slack_user_slack_id, emoji
        ):
            return None
        row = SlackReaction(
            slack_message_id=_stored_message_id(
                session, slack_channel_slack_id, message_ts
            ),
            slack_channel_slack_id=slack_channel_slack_id,
            message_ts=message_ts,
            slack_user_slack_id=slack_user_slack_id,
            emoji=emoji,
            added_at=added_at,
            added_by_backfill=added_by_backfill,
            is_bot=is_bot,
        )
        session.add(row)
        try:
            session.commit()
        except IntegrityError:
            # concurrent redelivery won the race
            session.rollback()
            return None
        session.refresh(row)
        return row


def remove_reaction(
    slack_channel_slack_id: str,
    message_ts: str,
    slack_user_slack_id: str,
    emoji: str,
    removed_at: Optional[datetime.datetime] = None,
    removed_by_backfill: bool = False,
    session: Optional[Session] = None,
) -> bool:
    """Stamp removed_at on the active row; False when none is active."""
    emoji = normalize_emoji(emoji)
    with SessionManager(session) as session:
        row = _active(
            session, slack_channel_slack_id, message_ts, slack_user_slack_id, emoji
        )
        if row is None:
            return False
        row.removed_at = removed_at or datetime.datetime.now(datetime.timezone.utc)
        row.removed_by_backfill = removed_by_backfill
        session.add(row)
        session.commit()
        return True


def list_active_reactions(
    slack_channel_slack_id: str,
    message_ts: str,
    session: Optional[Session] = None,
) -> list[SlackReaction]:
    with SessionManager(session) as session:
        return list(
            session.exec(
                select(SlackReaction)
                .where(SlackReaction.slack_channel_slack_id == slack_channel_slack_id)
                .where(SlackReaction.message_ts == message_ts)
                .where(SlackReaction.removed_at.is_(None))  # type: ignore[union-attr]
                .order_by(SlackReaction.id)  # type: ignore[arg-type]
            ).all()
        )
