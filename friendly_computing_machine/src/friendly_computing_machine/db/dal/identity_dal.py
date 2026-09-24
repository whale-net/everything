"""Slack -> Keycloak identity linking DAL.

Stores the one Slack-to-Keycloak identity mapping per Slack user, plus the
one-time link tokens that carry a link attempt across the Keycloak redirect.
Only the Keycloak (iss, sub) pair is persisted - never a Keycloak token.
"""

import datetime
import logging
import secrets
from typing import Optional

from sqlalchemy import update
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackKeycloakIdentity,
    SlackLinkToken,
)

logger = logging.getLogger(__name__)

# a link token is only meant to survive a Keycloak round-trip
LINK_TOKEN_TTL = datetime.timedelta(minutes=10)


def _now() -> datetime.datetime:
    return datetime.datetime.now(datetime.UTC)


def get_keycloak_identity(
    slack_team_id: str,
    slack_user_id: str,
    session: Optional[Session] = None,
) -> SlackKeycloakIdentity | None:
    """Get the Keycloak identity linked to a Slack user, if any."""
    with SessionManager(session) as session:
        return session.exec(
            select(SlackKeycloakIdentity).where(
                SlackKeycloakIdentity.slack_team_id == slack_team_id,
                SlackKeycloakIdentity.slack_user_id == slack_user_id,
            )
        ).first()


def mint_link_token(
    slack_team_id: str,
    slack_user_id: str,
    ttl: datetime.timedelta = LINK_TOKEN_TTL,
    session: Optional[Session] = None,
) -> str:
    """Create a one-time link token for a Slack user and return its value."""
    now = _now()
    token = secrets.token_urlsafe(32)
    with SessionManager(session) as session:
        session.add(
            SlackLinkToken(
                token=token,
                slack_team_id=slack_team_id,
                slack_user_id=slack_user_id,
                expires_at=now + ttl,
            )
        )
        session.commit()
    return token


def peek_link_token(
    token: str,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> SlackLinkToken | None:
    """Look up a link token without consuming it.

    Returns None if the token is unknown, expired, or already consumed.
    """
    now = now or _now()
    with SessionManager(session) as session:
        return session.exec(
            select(SlackLinkToken).where(
                SlackLinkToken.token == token,
                SlackLinkToken.consumed.is_(False),
                SlackLinkToken.expires_at > now,
            )
        ).first()


def complete_link(
    token: str,
    keycloak_iss: str,
    keycloak_sub: str,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> SlackKeycloakIdentity | None:
    """Redeem a link token and write the Slack -> Keycloak mapping.

    Consuming the token and upserting the mapping happen in a single
    transaction, so a concurrent double-submit of the same token yields
    exactly one success. Returns None (writing nothing) when the token is
    unknown, expired, or already consumed.
    """
    now = now or _now()
    with SessionManager(session) as session:
        # conditional UPDATE is the atomicity gate: only one racing caller can
        # flip consumed false -> true, and only that caller sees rowcount 1.
        # synchronize_session=False: the consume is a raw conditional flip whose
        # only output we read is rowcount, so skip the ORM's Python-side WHERE
        # evaluation (which mis-compares naive vs aware datetimes on backends
        # that don't preserve tz). We re-read the row and refresh below.
        result = session.execute(
            update(SlackLinkToken)
            .where(
                SlackLinkToken.token == token,
                SlackLinkToken.consumed.is_(False),
                SlackLinkToken.expires_at > now,
            )
            .values(consumed=True, consumed_at=now),
            execution_options={"synchronize_session": False},
        )
        if result.rowcount != 1:
            session.rollback()
            return None

        row = session.exec(
            select(SlackLinkToken).where(SlackLinkToken.token == token)
        ).first()
        identity = _upsert_identity(
            session, row.slack_team_id, row.slack_user_id, keycloak_iss, keycloak_sub
        )
        session.commit()
        session.refresh(identity)
        return identity


def _upsert_identity(
    session: Session,
    slack_team_id: str,
    slack_user_id: str,
    keycloak_iss: str,
    keycloak_sub: str,
) -> SlackKeycloakIdentity:
    """Overwrite (or create) the mapping for one Slack identity.

    Re-linking a user replaces their (iss, sub) instead of stacking rows.
    """
    identity = session.exec(
        select(SlackKeycloakIdentity).where(
            SlackKeycloakIdentity.slack_team_id == slack_team_id,
            SlackKeycloakIdentity.slack_user_id == slack_user_id,
        )
    ).first()
    if identity is not None:
        identity.keycloak_iss = keycloak_iss
        identity.keycloak_sub = keycloak_sub
        return identity
    identity = SlackKeycloakIdentity(
        slack_team_id=slack_team_id,
        slack_user_id=slack_user_id,
        keycloak_iss=keycloak_iss,
        keycloak_sub=keycloak_sub,
    )
    session.add(identity)
    session.flush()
    return identity
