"""Bolt-free poll publishing.

The `/wpoll` create -> post -> set-ts path, extracted from the bolt
command handler so the Temporal music-poll worker can publish a poll
without importing the bolt app.
"""

import logging
from dataclasses import dataclass

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
    PollSnapshot,
    create_poll,
    set_poll_message,
)

logger = logging.getLogger(__name__)

_POST_ERROR = (
    "I couldn't post the poll here. Invite me to this channel and try again."
)


@dataclass
class ScheduledPollPost:
    """The ids a scheduled run's history row is stamped with."""

    poll_id: int
    slack_message_ts: str


def _store_and_post(
    client: SlackWebClientFCM,
    spec: PollSpec,
    channel_id: str,
    user_id: str,
    automated: bool = False,
) -> tuple[PollSnapshot, str | None, str | None]:
    """Create the poll row and post it.

    Returns (snapshot, slack_message_ts, error): the ts is set once
    the Slack post succeeded, and error is a user-facing message
    when the post failed.
    """
    snapshot = create_poll(
        question=spec.question,
        options=spec.options,
        slack_channel_slack_id=channel_id,
        creator_slack_user_slack_id=user_id,
        anonymous=spec.anonymous,
        vote_limit=spec.vote_limit,
        automated=automated,
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
        return snapshot, None, _POST_ERROR

    set_poll_message(snapshot.poll.id, response["channel"], response["ts"])
    logger.info("poll %s created by %s in %s", snapshot.poll.id, user_id, channel_id)
    return snapshot, response["ts"], None


def publish_poll(
    client: SlackWebClientFCM, spec: PollSpec, channel_id: str, user_id: str
) -> str | None:
    """Store and post a poll; returns a user-facing error if it could not be posted."""
    _snapshot, _slack_message_ts, error = _store_and_post(
        client, spec, channel_id, user_id
    )
    return error


def publish_scheduled_poll(
    client: SlackWebClientFCM,
    spec: PollSpec,
    channel_id: str,
    bot_user_id: str,
) -> ScheduledPollPost | str:
    """Store and post a scheduled poll as the bot.

    Returns the poll ids the run's scheduledpollrun row is stamped
    with, or a user-facing error when the post failed.
    """
    snapshot, slack_message_ts, error = _store_and_post(
        client, spec, channel_id, bot_user_id, automated=True
    )
    if error is not None:
        return error
    assert slack_message_ts is not None  # set on the success path only
    return ScheduledPollPost(
        poll_id=snapshot.poll.id, slack_message_ts=slack_message_ts
    )
