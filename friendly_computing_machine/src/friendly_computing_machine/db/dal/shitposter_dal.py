"""Shitposter DAL: SCD2 channel opt-in, workspace kill switch, persona
revisions and post records.

Control-plane reads always hit the database directly (never the cached bot
config).
"""

import datetime
import logging
from dataclasses import dataclass
from typing import Callable, Literal, Optional

from sqlalchemy import func, update
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    KILL_SWITCH_SCOPE,
    ShitposterChannelOptIn,
    ShitposterKillSwitch,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
)

logger = logging.getLogger(__name__)

GateReason = Literal["not_opted_in", "silenced"]


@dataclass(frozen=True)
class GateResult:
    allowed: bool
    reason: Optional[GateReason] = None


# (event, slack_channel_slack_id | None); event is optin/optout/resume
StateChangeNotifier = Callable[[str, Optional[str]], None]


def _noop_notifier(event: str, slack_channel_slack_id: Optional[str]) -> None:
    pass


_notifier: StateChangeNotifier = _noop_notifier


def set_state_change_notifier(notifier: Optional[StateChangeNotifier]) -> None:
    """Install the callback fired on opt-in, opt-out and resume."""
    global _notifier
    _notifier = notifier or _noop_notifier


def _notify(event: str, slack_channel_slack_id: Optional[str]) -> None:
    try:
        _notifier(event, slack_channel_slack_id)
    except Exception:
        logger.exception("shitposter state-change notifier failed for %s", event)


def _current_opt_in(
    session: Session, channel_db_id: int
) -> Optional[ShitposterChannelOptIn]:
    return session.exec(
        select(ShitposterChannelOptIn)
        .where(ShitposterChannelOptIn.slack_channel_id == channel_db_id)
        .where(ShitposterChannelOptIn.valid_to.is_(None))  # type: ignore[union-attr]
    ).one_or_none()


def set_channel_opt_in(
    slack_channel_slack_id: str,
    opted_in: bool,
    actor_slack_user_id: str,
    actor_slack_team_id: Optional[str] = None,
    session: Optional[Session] = None,
    channel_name: Optional[str] = None,
) -> bool:
    """SCD2 close-and-open in one transaction. Returns False on a no-op."""
    with SessionManager(session) as session:
        channel = session.exec(
            select(SlackChannel).where(SlackChannel.slack_id == slack_channel_slack_id)
        ).one_or_none()
        if channel is None:
            channel = SlackChannel(
                slack_id=slack_channel_slack_id,
                name=channel_name or slack_channel_slack_id,
                channel_type="channel",
            )
            session.add(channel)
            session.flush()
        current = _current_opt_in(session, channel.id)
        if current is not None and current.opted_in == opted_in:
            session.rollback()
            return False
        if current is None and not opted_in:
            # never opted in; opting out is already the effective state
            session.rollback()
            return False
        if current is not None:
            current.valid_to = func.now()  # type: ignore[assignment]
            session.add(current)
            session.flush()
        session.add(
            ShitposterChannelOptIn(
                slack_channel_id=channel.id,
                opted_in=opted_in,
                set_by_slack_user_id=actor_slack_user_id,
                set_by_slack_team_id=actor_slack_team_id,
            )
        )
        session.commit()
    logger.info(
        "shitposter channel opt-in changed: channel=%s actor=%s opted_in=%s",
        slack_channel_slack_id,
        actor_slack_user_id,
        opted_in,
    )
    _notify("optin" if opted_in else "optout", slack_channel_slack_id)
    return True


def set_kill_switch(
    enabled: bool,
    actor: str,
    reason: Optional[str] = None,
    session: Optional[Session] = None,
) -> bool:
    """SCD2 close-and-open in one transaction. Returns False on a no-op."""
    with SessionManager(session) as session:
        current = session.exec(
            select(ShitposterKillSwitch)
            .where(ShitposterKillSwitch.scope == KILL_SWITCH_SCOPE)
            .where(ShitposterKillSwitch.valid_to.is_(None))  # type: ignore[union-attr]
        ).one_or_none()
        effective = True if current is None else current.enabled
        if effective == enabled:
            return False
        if current is not None:
            current.valid_to = func.now()  # type: ignore[assignment]
            session.add(current)
            session.flush()
        session.add(
            ShitposterKillSwitch(
                scope=KILL_SWITCH_SCOPE, enabled=enabled, set_by=actor, reason=reason
            )
        )
        session.commit()
    logger.info("shitposter kill switch changed: actor=%s enabled=%s", actor, enabled)
    if enabled:
        _notify("resume", None)
    return True


def list_channel_opt_in_history(
    slack_channel_slack_id: str, session: Optional[Session] = None
) -> list[ShitposterChannelOptIn]:
    """All opt-in rows for the channel, oldest first."""
    with SessionManager(session) as session:
        return list(
            session.exec(
                select(ShitposterChannelOptIn)
                .join(
                    SlackChannel,
                    SlackChannel.id == ShitposterChannelOptIn.slack_channel_id,  # type: ignore[arg-type]
                )
                .where(SlackChannel.slack_id == slack_channel_slack_id)
                .order_by(ShitposterChannelOptIn.id)  # type: ignore[arg-type]
            ).all()
        )


def list_kill_switch_history(
    session: Optional[Session] = None,
) -> list[ShitposterKillSwitch]:
    """All kill-switch rows, oldest first."""
    with SessionManager(session) as session:
        return list(
            session.exec(
                select(ShitposterKillSwitch).order_by(ShitposterKillSwitch.id)  # type: ignore[arg-type]
            ).all()
        )


def is_channel_opted_in(
    slack_channel_slack_id: str, session: Optional[Session] = None
) -> bool:
    """Single uncached query against the current opt-in row."""
    with SessionManager(session) as session:
        row = session.exec(
            select(ShitposterChannelOptIn.opted_in)
            .join(
                SlackChannel,
                SlackChannel.id == ShitposterChannelOptIn.slack_channel_id,  # type: ignore[arg-type]
            )
            .where(SlackChannel.slack_id == slack_channel_slack_id)
            .where(ShitposterChannelOptIn.valid_to.is_(None))  # type: ignore[union-attr]
        ).first()
        return bool(row)


def list_opted_in_channel_slack_ids(session: Optional[Session] = None) -> set[str]:
    """Slack ids of channels whose current opt-in row is opted in (uncached)."""
    with SessionManager(session) as session:
        return set(
            session.exec(
                select(SlackChannel.slack_id)
                .join(
                    ShitposterChannelOptIn,
                    SlackChannel.id == ShitposterChannelOptIn.slack_channel_id,  # type: ignore[arg-type]
                )
                .where(ShitposterChannelOptIn.opted_in.is_(True))  # type: ignore[attr-defined]
                .where(ShitposterChannelOptIn.valid_to.is_(None))  # type: ignore[union-attr]
            ).all()
        )


def is_shitposter_enabled(session: Optional[Session] = None) -> bool:
    """True unless a current kill-switch row says disabled."""
    with SessionManager(session) as session:
        row = session.exec(
            select(ShitposterKillSwitch.enabled)
            .where(ShitposterKillSwitch.scope == KILL_SWITCH_SCOPE)
            .where(ShitposterKillSwitch.valid_to.is_(None))  # type: ignore[union-attr]
        ).first()
        return True if row is None else bool(row)


def shitposter_gate(
    slack_channel_slack_id: str, session: Optional[Session] = None
) -> GateResult:
    """Gate for every capture/posting/summon/riff path."""
    with SessionManager(session) as session:
        if not is_shitposter_enabled(session):
            return GateResult(False, "silenced")
        if not is_channel_opted_in(slack_channel_slack_id, session):
            return GateResult(False, "not_opted_in")
        return GateResult(True)


DEFAULT_PERSONA_NAME = "shitposter"


def get_default_persona(session: Optional[Session] = None) -> ShitposterPersona | None:
    """Return the seeded fixed persona (lowest id)."""
    with SessionManager(session) as session:
        return session.exec(
            select(ShitposterPersona).order_by(ShitposterPersona.id)
        ).first()


def get_current_persona_revision(
    persona_id: int, session: Optional[Session] = None
) -> ShitposterPersonaRevision | None:
    """Return the persona's open (valid_to IS NULL) revision."""
    with SessionManager(session) as session:
        return session.exec(
            select(ShitposterPersonaRevision).where(
                ShitposterPersonaRevision.persona_id == persona_id,
                ShitposterPersonaRevision.valid_to.is_(None),
            )
        ).first()


def list_persona_revisions(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterPersonaRevision]:
    """Return all revisions of a persona, oldest first, with valid_from/valid_to."""
    with SessionManager(session) as session:
        return list(
            session.exec(
                select(ShitposterPersonaRevision)
                .where(ShitposterPersonaRevision.persona_id == persona_id)
                .order_by(
                    ShitposterPersonaRevision.valid_from,
                    ShitposterPersonaRevision.id,
                )
            ).all()
        )


def add_persona_revision(
    persona_id: int,
    persona_text: str,
    cause_kind: str | None = None,
    cause_ref: str | None = None,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterPersonaRevision:
    """Close the current revision and open a new one in one transaction."""
    now = now or datetime.datetime.now(datetime.UTC)
    with SessionManager(session) as session:
        session.exec(
            update(ShitposterPersonaRevision)
            .where(
                ShitposterPersonaRevision.persona_id == persona_id,
                ShitposterPersonaRevision.valid_to.is_(None),
            )
            .values(valid_to=now)
            .execution_options(synchronize_session=False)
        )
        revision = ShitposterPersonaRevision(
            persona_id=persona_id,
            persona_text=persona_text,
            cause_kind=cause_kind,
            cause_ref=cause_ref,
            valid_from=now,
        )
        session.add(revision)
        session.commit()
        session.refresh(revision)
        return revision


def record_post(
    slack_channel_id: int,
    slack_message_ts: str,
    persona_id: int,
    persona_revision_id: int,
    trigger: str,
    principal_iss: str,
    principal_sub: str,
    principal_kind: str,
    whagent_session_id: str,
    thread_ts: str | None = None,
    thread_owner_slack_user_id: str | None = None,
    parent_post_id: int | None = None,
    session: Optional[Session] = None,
) -> ShitposterPost:
    """Persist a bot post; (channel, ts) is unique and a duplicate raises."""
    with SessionManager(session) as session:
        post = ShitposterPost(
            slack_channel_id=slack_channel_id,
            slack_message_ts=slack_message_ts,
            thread_ts=thread_ts,
            persona_id=persona_id,
            persona_revision_id=persona_revision_id,
            trigger=str(getattr(trigger, "value", trigger)),
            principal_iss=principal_iss,
            principal_sub=principal_sub,
            principal_kind=str(getattr(principal_kind, "value", principal_kind)),
            whagent_session_id=whagent_session_id,
            thread_owner_slack_user_id=thread_owner_slack_user_id,
            parent_post_id=parent_post_id,
        )
        session.add(post)
        session.commit()
        session.refresh(post)
        return post


def get_post_by_channel_ts(
    slack_channel_id: int, slack_message_ts: str, session: Optional[Session] = None
) -> ShitposterPost | None:
    """Look up a bot post by channel and Slack ts (e.g. a thread root ts)."""
    with SessionManager(session) as session:
        return session.exec(
            select(ShitposterPost).where(
                ShitposterPost.slack_channel_id == slack_channel_id,
                ShitposterPost.slack_message_ts == slack_message_ts,
            )
        ).first()


def set_thread_owner_if_unset(
    post_id: int, slack_user_id: str, session: Optional[Session] = None
) -> bool:
    """Atomically claim thread ownership; True only for the winning caller."""
    with SessionManager(session) as session:
        result = session.exec(
            update(ShitposterPost)
            .where(
                ShitposterPost.id == post_id,
                ShitposterPost.thread_owner_slack_user_id.is_(None),
            )
            .values(thread_owner_slack_user_id=slack_user_id)
            .execution_options(synchronize_session=False)
        )
        session.commit()
        return result.rowcount == 1
