"""Bolt-free poll publishing.

The `/wpoll` create -> post -> set-ts path, extracted from the bolt
command handler so the Temporal music-poll worker can publish a poll
without importing the bolt app.
"""

import logging

from opentelemetry import trace
from slack_sdk.errors import SlackApiError

from friendly_computing_machine.src.friendly_computing_machine.bot.poll.parse import (
    PollSpec,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.poll.render import (
    render_poll_blocks,
    render_poll_text,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_client import (
    SlackWebClientFCM,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.poll_dal import (
    create_poll,
    set_poll_message,
)

logger = logging.getLogger(__name__)


def publish_poll(
    client: SlackWebClientFCM, spec: PollSpec, channel_id: str, user_id: str
) -> str | None:
    """Store and post a poll; returns a user-facing error if it could not be posted."""
    snapshot = create_poll(
        question=spec.question,
        options=spec.options,
        slack_channel_slack_id=channel_id,
        creator_slack_user_slack_id=user_id,
        anonymous=spec.anonymous,
        vote_limit=spec.vote_limit,
    )
    trace.get_current_span().set_attribute("db.poll.id", snapshot.poll.id)
    try:
        response = client.chat_postMessage(
            channel=channel_id,
            text=render_poll_text(snapshot),
            blocks=render_poll_blocks(snapshot),
        )
    except SlackApiError as e:
        # most commonly the bot has not been invited to the channel
        logger.warning(
            "could not post poll %s to %s: %s",
            snapshot.poll.id,
            channel_id,
            e.response.get("error"),
        )
        return "I couldn't post the poll here. Invite me to this channel and try again."

    set_poll_message(snapshot.poll.id, response["channel"], response["ts"])
    logger.info("poll %s created by %s in %s", snapshot.poll.id, user_id, channel_id)
    return None
