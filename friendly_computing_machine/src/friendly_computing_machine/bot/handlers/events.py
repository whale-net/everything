import datetime
import logging

from opentelemetry import trace

from friendly_computing_machine.src.friendly_computing_machine.bot.app import app, get_bot_config
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers.riff import (
    riff_thread_reply,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers.whagent import (
    relay_thread_reply,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    add_reaction,
    get_bot_slack_user_slack_ids,
    is_channel_opted_in,
    remove_reaction,
    upsert_message,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import SlackMessageCreate

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)


@app.event("message")
def handle_message(event, say, client=None):
    # TODO: typehint for event? or am I supposed to just yolo it?
    with tracer.start_as_current_span("handle_message") as span:
        try:
            logger.debug(event)

            # Bolt runs only the first matching "message" listener, so the
            # whagent thread relay and shitposter riffs are dispatched from here.
            relayed = False
            try:
                relayed = relay_thread_reply(event)
                span.set_attribute("whagent.relayed", relayed)
            except Exception:
                logger.exception("failed to relay thread reply to whagent-net")

            if not relayed:
                try:
                    span.set_attribute("shitposter.riff", riff_thread_reply(event, client))
                except Exception:
                    logger.exception("failed to handle shitposter thread riff")

            sub_type = event.get("subtype", "")
            span.set_attribute("slack.event.subtype", sub_type)

            if sub_type == "message_changed":
                # TODO - update message - for now, will use latest message for poll
                logger.info(
                    "message update received. not updating, not implemented (yet)"
                )
                span.set_attribute("message.processed", False)
                span.set_attribute("message.reason", "message_changed subtype")
                return
            else:
                message_event = event

            message = SlackMessageCreate.from_slack_message_json(message_event)
            span.set_attribute("slack.message.id", message.slack_id)
            span.set_attribute("slack.channel.id", message.slack_channel_slack_id)
            span.set_attribute("slack.user.id", message.slack_user_slack_id)

            # Rules for inserting messages
            # is in channel.is_music_poll
            # there used to be a rule about bot user, bot thread, but that was removed
            config = get_bot_config()

            music_poll_channel = message.slack_channel_slack_id in {
                info.slack_channel.slack_id for info in config.music_poll_infos
            }
            # opt-in is read live, never from the cached bot config
            if not music_poll_channel and not is_channel_opted_in(
                message.slack_channel_slack_id
            ):
                # A relayed whagent turn was already handled above -- it's not
                # dropped, it's just not a stored channel, so don't log
                # it as skipped.
                if not relayed:
                    logger.info(
                        "skipping message %s - channel not stored",
                        message.slack_id,
                    )
                span.set_attribute("message.processed", False)
                span.set_attribute("message.reason", "channel not stored")
                return

            # if we reach this point, we can insert the message
            # will be processed later
            msg = upsert_message(message)
            span.set_attribute("db.message.id", msg.id)
            span.set_attribute("message.processed", True)
            logger.info("message inserted. id=%s, slack_id=%s", msg.id, msg.slack_id)
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            raise


def _event_time(event_ts: str | None) -> datetime.datetime | None:
    if not event_ts:
        return None
    try:
        return datetime.datetime.fromtimestamp(float(event_ts), tz=datetime.timezone.utc)
    except (TypeError, ValueError):
        return None


def _capture_reaction(event, name: str, apply) -> None:
    """Shared gate for reaction events: message items in opted-in channels only."""
    with tracer.start_as_current_span(name) as span:
        try:
            item = event.get("item") or {}
            channel = item.get("channel")
            span.set_attribute("slack.channel.id", channel or "")
            span.set_attribute("slack.user.id", event.get("user") or "")
            if item.get("type") != "message" or not channel or not item.get("ts"):
                logger.debug("ignoring reaction on non-message item: %s", item)
                span.set_attribute("reaction.processed", False)
                return
            if not is_channel_opted_in(channel):
                logger.debug("ignoring reaction in non-opted-in channel %s", channel)
                span.set_attribute("reaction.processed", False)
                return
            changed = apply(channel, item["ts"], event)
            span.set_attribute("reaction.processed", True)
            span.set_attribute("reaction.changed", bool(changed))
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            raise


@app.event("reaction_added")
def handle_reaction_added(event):
    def apply(channel, ts, event):
        user = event.get("user")
        try:
            is_bot = user in get_bot_slack_user_slack_ids()
        except Exception:
            is_bot = None
        return add_reaction(
            channel,
            ts,
            user,
            event.get("reaction"),
            added_at=_event_time(event.get("event_ts")),
            is_bot=is_bot,
        )

    _capture_reaction(event, "handle_reaction_added", apply)


@app.event("reaction_removed")
def handle_reaction_removed(event):
    def apply(channel, ts, event):
        return remove_reaction(
            channel,
            ts,
            event.get("user"),
            event.get("reaction"),
            removed_at=_event_time(event.get("event_ts")),
        )

    _capture_reaction(event, "handle_reaction_removed", apply)


@app.error
def global_error_handler(error, body, logger):
    """Handles errors globally."""
    logger.exception(f"Error: {error}")
    logger.info(f"Request body: {body}")
