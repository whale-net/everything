"""Shitposter persona (SCD2 revisions) and bot post records DAL."""

import datetime
from typing import Optional

from sqlalchemy import update
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
)

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
