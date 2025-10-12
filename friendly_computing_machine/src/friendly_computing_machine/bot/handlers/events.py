import logging

from opentelemetry import trace

from friendly_computing_machine.bot.app import app, get_bot_config
from friendly_computing_machine.db.dal import upsert_message
from friendly_computing_machine.models.slack import SlackMessageCreate

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)


@app.event("message")
def handle_message(event, say):
    # TODO: typehint for event? or am I supposed to just yolo it?
    with tracer.start_as_current_span("handle_message") as span:
        try:
            logger.debug(event)
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

            if message.slack_channel_slack_id not in {
                info.slack_channel.slack_id for info in config.music_poll_infos
            }:
                logger.info(
                    "skipping message %s - not in music poll channel", message.slack_id
                )
                span.set_attribute("message.processed", False)
                span.set_attribute("message.reason", "not in music poll channel")
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


@app.error
def global_error_handler(error, body, logger):
    """Handles errors globally."""
    logger.exception(f"Error: {error}")
    logger.info(f"Request body: {body}")
