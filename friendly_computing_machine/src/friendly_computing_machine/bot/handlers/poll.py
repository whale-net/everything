"""`/wpoll` slash command and its vote/close button handlers.

All interactions arrive over the bolt Socket Mode connection; each vote is
persisted, then the poll message is re-rendered in place with `chat.update`.
"""

import datetime
import json
import logging
import re
import threading
from collections import defaultdict
from dataclasses import asdict, replace

from opentelemetry import trace
from slack_bolt import Ack, Respond

from friendly_computing_machine.src.friendly_computing_machine.bot.app import app
from friendly_computing_machine.src.friendly_computing_machine.bot.poll.modal import (
    POLL_ADD_OPTION_ACTION,
    POLL_MODAL_CALLBACK,
    QUESTION_BLOCK,
    PollModalError,
    PollModalMetadata,
    build_poll_modal,
    parse_poll_modal,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.poll.parse import (
    MAX_OPTIONS,
    USAGE,
    PollParseError,
    parse_poll_command,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.poll.render import (
    POLL_CLOSE_ACTION,
    POLL_VOTE_ACTION_PREFIX,
    decode_vote_value,
    render_poll_blocks,
    render_poll_text,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_client import (
    SlackWebClientFCM,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    insert_slack_command,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.poll_dal import (
    PollSnapshot,
    VoteOutcome,
    cast_poll_vote,
    close_poll,
    get_poll_snapshot,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackCommandCreate,
)
from friendly_computing_machine.src.friendly_computing_machine.poll.publish import (
    publish_poll,
)

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)

POLL_COMMAND = "/wpoll"

# Serializes vote -> render -> chat.update per poll so a stale render never wins.
_poll_locks: defaultdict[int, threading.Lock] = defaultdict(threading.Lock)


def _refresh_message(client: SlackWebClientFCM, snapshot: PollSnapshot) -> None:
    poll = snapshot.poll
    if poll.slack_message_ts is None:
        logger.warning("poll %s has no message to update", poll.id)
        return
    client.chat_update(
        channel=poll.slack_channel_slack_id,
        ts=poll.slack_message_ts,
        text=render_poll_text(snapshot),
        blocks=render_poll_blocks(snapshot),
    )


def _record_command(user_id: str, channel_id: str, text: str) -> None:
    insert_slack_command(
        SlackCommandCreate(
            caller_slack_user_id=user_id,
            command_base=POLL_COMMAND,
            command_text=text,
            slack_channel_slack_id=channel_id,
            created_at=datetime.datetime.now(),
        )
    )


@app.command(POLL_COMMAND)
def handle_poll_command(ack: Ack, respond: Respond, command, client: SlackWebClientFCM):
    with tracer.start_as_current_span("handle_poll_command") as span:
        user_id = command["user_id"]
        channel_id = command["channel_id"]
        text = command.get("text", "")
        span.set_attribute("slack.command", POLL_COMMAND)
        span.set_attribute("slack.user.id", user_id)
        span.set_attribute("slack.channel.id", channel_id)

        if not text.strip():
            ack()
            client.views_open(
                trigger_id=command["trigger_id"],
                view=build_poll_modal(PollModalMetadata(channel_id=channel_id)),
            )
            return

        try:
            spec = parse_poll_command(text)
        except PollParseError as e:
            ack(text=f"{e}\n{USAGE}")
            return
        ack()

        _record_command(user_id, channel_id, text)
        error = publish_poll(client, spec, channel_id, user_id)
        if error:
            respond(text=error, response_type="ephemeral")


@app.action(POLL_ADD_OPTION_ACTION)
def handle_poll_add_option(ack: Ack, body, client: SlackWebClientFCM):
    ack()
    with tracer.start_as_current_span("handle_poll_add_option") as span:
        span.set_attribute("slack.user.id", body["user"]["id"])
        view = body["view"]
        metadata = PollModalMetadata.loads(view["private_metadata"])
        span.set_attribute("slack.channel.id", metadata.channel_id)
        metadata = replace(
            metadata, option_fields=min(metadata.option_fields + 1, MAX_OPTIONS)
        )
        # unchanged block_ids keep what the user already typed
        client.views_update(
            view_id=view["id"], hash=view["hash"], view=build_poll_modal(metadata)
        )


@app.view(POLL_MODAL_CALLBACK)
def handle_poll_modal_submit(ack: Ack, body, view, client: SlackWebClientFCM):
    with tracer.start_as_current_span("handle_poll_modal_submit") as span:
        user_id = body["user"]["id"]
        channel_id = PollModalMetadata.loads(view["private_metadata"]).channel_id
        span.set_attribute("slack.user.id", user_id)
        span.set_attribute("slack.channel.id", channel_id)
        try:
            spec = parse_poll_modal(view)
        except PollModalError as e:
            ack(response_action="errors", errors=e.errors)
            return

        _record_command(user_id, channel_id, json.dumps(asdict(spec)))
        # post before acking so a failure can be shown in the still-open modal
        error = publish_poll(client, spec, channel_id, user_id)
        if error:
            ack(response_action="errors", errors={QUESTION_BLOCK: error})
            return
        ack()


@app.action(re.compile(f"^{POLL_VOTE_ACTION_PREFIX}"))
def handle_poll_vote(ack: Ack, body, respond: Respond, client: SlackWebClientFCM):
    ack()
    with tracer.start_as_current_span("handle_poll_vote") as span:
        user_id = body["user"]["id"]
        poll_id, poll_option_id = decode_vote_value(body["actions"][0]["value"])
        span.set_attribute("slack.user.id", user_id)
        span.set_attribute("db.poll.id", poll_id)

        with _poll_locks[poll_id]:
            outcome, snapshot = cast_poll_vote(poll_id, poll_option_id, user_id)
            span.set_attribute("poll.vote.outcome", str(outcome))
            if snapshot is not None:
                _refresh_message(client, snapshot)

        if outcome == VoteOutcome.LIMIT_REACHED:
            respond(
                text=(
                    f"You've used all {snapshot.poll.vote_limit} of your votes. "
                    "Click one of your choices to remove it first."
                ),
                response_type="ephemeral",
                replace_original=False,
            )
        elif outcome == VoteOutcome.CLOSED:
            respond(
                text="This poll is closed.",
                response_type="ephemeral",
                replace_original=False,
            )
        elif outcome == VoteOutcome.NOT_FOUND:
            logger.warning(
                "vote for unknown poll %s option %s", poll_id, poll_option_id
            )


@app.action(POLL_CLOSE_ACTION)
def handle_poll_close(ack: Ack, body, respond: Respond, client: SlackWebClientFCM):
    ack()
    with tracer.start_as_current_span("handle_poll_close") as span:
        user_id = body["user"]["id"]
        poll_id = int(body["actions"][0]["value"])
        span.set_attribute("slack.user.id", user_id)
        span.set_attribute("db.poll.id", poll_id)

        with _poll_locks[poll_id]:
            snapshot = get_poll_snapshot(poll_id)
            if snapshot is None:
                logger.warning("close requested for unknown poll %s", poll_id)
                return
            if snapshot.poll.creator_slack_user_slack_id != user_id:
                respond(
                    text="Only the person who created this poll can close it.",
                    response_type="ephemeral",
                    replace_original=False,
                )
                return
            snapshot = close_poll(poll_id)
            _refresh_message(client, snapshot)
        logger.info("poll %s closed by %s", poll_id, user_id)
