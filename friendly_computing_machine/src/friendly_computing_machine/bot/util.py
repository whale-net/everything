import logging
from datetime import datetime
from typing import List, Optional, Union

from slack_sdk.models.blocks import Block

from friendly_computing_machine.bot.app import get_slack_web_client
from friendly_computing_machine.bot.slack_models import render_blocks_to_text
from friendly_computing_machine.db.dal import insert_message
from friendly_computing_machine.models.slack import SlackMessage, SlackMessageCreate
from friendly_computing_machine.util import ts_to_datetime

logger = logging.getLogger(__name__)


def slack_send_message(
    channel: str,
    message: Union[str, List] = None,
    blocks: Optional[list[Block]] = None,
    thread_ts: Optional[datetime] = None,
    update_ts: Optional[str] = None,
) -> SlackMessage:
    """
    Send a message to Slack channel with either text or blocks.

    Args:
        channel: Slack channel ID or name
        message: Plain text message (deprecated - use blocks for rich messages)
        blocks: List of Slack block objects for rich messages
        thread_ts: Optional thread timestamp for threaded replies
        update_ts: Optional message timestamp to update instead of posting new

    Returns:
        SlackMessage object representing the sent message
    """
    web_client = get_slack_web_client()
    # Handle backward compatibility and determine message format
    if blocks is not None:
        # Use blocks parameter (new behavior)
        message_blocks = blocks
        message_text = render_blocks_to_text(blocks)
        logger.info("sending message with blocks: %s", message_text[:100])
    elif isinstance(message, list):
        # Handle list passed as message parameter
        message_blocks = message
        message_text = render_blocks_to_text(message)
        logger.info("sending message with blocks: %s", message_text[:100])
    elif message:
        # Plain text message (legacy behavior)
        message_blocks = None
        message_text = message or ""
        logger.info("sending message: %s", message_text)
    else:
        # No message provided, raise an error
        raise ValueError("Either 'message' or 'blocks' must be provided")

    thread_ts_str = None
    if thread_ts is not None:
        thread_ts_str = str(thread_ts.timestamp())

    # Build message arguments
    message_args = {
        "channel": channel,
        "text": message_text,
    }

    if message_blocks is not None:
        # even when blocks is present, include text as fallback - Slack recommends this for accessibility
        message_args["blocks"] = message_blocks

    # Update or send message to Slack
    if update_ts:
        # Update existing message
        message_args["ts"] = update_ts
        response = web_client.chat_update(**message_args)
        logger.info(
            "message updated, is response ok? %s", "yes" if response.get("ok") else "no"
        )
    else:
        # Send new message
        if thread_ts_str:
            message_args["thread_ts"] = thread_ts_str
        response = web_client.chat_postMessage(**message_args)
        logger.info(
            "message sent, is response ok? %s", "yes" if response.get("ok") else "no"
        )

    # our own messages aren't sent via event api
    # so need to insert them from the client response
    response_thread_ts = (
        response["message"].get("thread_ts")
        if "message" in response
        else response.get("thread_ts")
    )
    message_data = response.get("message", response)

    in_message = SlackMessageCreate(
        slack_id=None,
        slack_team_slack_id=message_data.get("team"),
        slack_channel_slack_id=response.get("channel"),
        slack_user_slack_id=message_data.get("user"),
        text=message_text,  # Store rendered text in database
        # unsure if message or response ts is more correct, or if it matters
        # TODO - check if this is giving wrong timezone
        ts=update_ts or ts_to_datetime(message_data.get("ts")),
        thread_ts=ts_to_datetime(response_thread_ts) if response_thread_ts else None,
        parent_user_slack_id=message_data.get("parent_user_id"),
    )
    logger.debug("in_message: %s", in_message)

    if update_ts:
        logger.warning(
            "Updating message with ts=%s, this will send a duplicate message in the database",
            update_ts,
        )
    out_message = insert_message(in_message)
    return out_message


def slack_bot_who_am_i():
    web_client = get_slack_web_client()
    return web_client.auth_test()
