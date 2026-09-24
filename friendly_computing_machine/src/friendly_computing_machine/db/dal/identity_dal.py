"""Slack -> Keycloak identity linking DAL.

Stores the one Slack-to-Keycloak identity mapping per Slack user, plus the
one-time link tokens that carry a link attempt across the Keycloak redirect.
Only the Keycloak (iss, sub) pair is persisted - never a Keycloak token.
"""

import datetime
import logging
from typing import Optional

from sqlmodel import Session

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


def get_keycloak_identity(
    slack_team_id: str,
    slack_user_id: str,
    session: Optional[Session] = None,
) -> SlackKeycloakIdentity | None:
    """Get the Keycloak identity linked to a Slack user, if any."""
    raise NotImplementedError


def mint_link_token(
    slack_team_id: str,
    slack_user_id: str,
    ttl: datetime.timedelta = LINK_TOKEN_TTL,
    session: Optional[Session] = None,
) -> str:
    """Create a one-time link token for a Slack user and return its value."""
    raise NotImplementedError


def peek_link_token(
    token: str,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> SlackLinkToken | None:
    """Look up a link token without consuming it.

    Returns None if the token is unknown, expired, or already consumed.
    """
    raise NotImplementedError


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
    raise NotImplementedError
