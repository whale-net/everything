"""Shared identity-link prompt for Slack entry points that act on behalf of a user."""

import logging
import os

from friendly_computing_machine.src.friendly_computing_machine.db.dal.identity_dal import (
    mint_link_token,
)

logger = logging.getLogger(__name__)


def web_public_url() -> str:
    """Base URL of the identity-link web app; the token is appended as /link/<token>."""
    return os.environ.get("FCM_WEB_PUBLIC_URL", "").rstrip("/")


def prompt_identity_link(
    client, channel_slack_id: str, team_id: str, slack_user_id: str, purpose: str
) -> bool:
    """Post a one-time link prompt ephemeral to the user; False if no URL is configured.

    Posted at the channel top level: a thread-scoped ephemeral only renders if
    the user has that thread open.
    """
    url = web_public_url()
    if not url:
        logger.error(
            "FCM_WEB_PUBLIC_URL is unset; cannot mint identity link "
            "for unlinked slack team=%s user=%s",
            team_id,
            slack_user_id,
        )
        return False
    token = mint_link_token(team_id, slack_user_id)
    client.chat_postEphemeral(
        channel=channel_slack_id,
        user=slack_user_id,
        text=(
            f"Link your Slack account to use {purpose}: "
            f"<{url}/link/{token}|Link my account> "
            "(one-time link, expires in 10 minutes)."
        ),
    )
    logger.info(
        "issued identity link prompt slack team=%s user=%s", team_id, slack_user_id
    )
    return True
