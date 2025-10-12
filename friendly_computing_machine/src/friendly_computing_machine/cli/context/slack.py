import logging
import os
from typing import Annotated

import typer

from friendly_computing_machine.bot.app import init_web_client

# Slack App Token - for Socket Mode (real-time events)
T_slack_app_token = Annotated[str, typer.Option(..., envvar="SLACK_APP_TOKEN")]
# Slack Bot Token - for Web API calls (posting messages, opening modals)
T_slack_bot_token = Annotated[str, typer.Option(..., envvar="SLACK_BOT_TOKEN")]
FILENAME = os.path.basename(__file__)
logger = logging.getLogger(__name__)


def setup_slack(
    ctx: typer.Context,
    slack_app_token: T_slack_app_token,
    slack_bot_token: T_slack_bot_token,
):
    """
    Setup Slack configuration with both tokens.

    Args:
        slack_app_token: For Socket Mode connection (real-time events)
        slack_bot_token: For Web API calls (messages, modals, etc.)
    """
    logger.debug("slack setup starting")
    ctx.obj[FILENAME] = {
        "slack_app_token": slack_app_token,
        "slack_bot_token": slack_bot_token,
    }
    init_web_client(slack_bot_token)
    logger.debug("slack setup complete")


def setup_slack_web_client_only(
    ctx: typer.Context,
    slack_bot_token: T_slack_bot_token,
):
    """
    Setup Slack configuration with only the bot token.

    Used by services that only need Web API access (like subscribe service)
    and don't need Socket Mode real-time events.

    Args:
        slack_bot_token: For Web API calls (messages, modals, etc.)
    """
    logger.debug("slack bot-only setup starting")
    ctx.obj[FILENAME] = {
        "slack_bot_token": slack_bot_token,
    }
    init_web_client(slack_bot_token)
    logger.debug("slack bot-only setup complete")
