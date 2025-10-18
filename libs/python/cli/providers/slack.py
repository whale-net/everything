"""Slack provider for bot and web API clients.

Provides Slack client context with both app token (Socket Mode) and bot token (Web API).

Example:
    ```python
    from libs.python.cli.providers.slack import SlackBotToken, create_slack_context

    app = typer.Typer()

    @app.callback()
    def setup(
        ctx: typer.Context,
        slack_bot_token: SlackBotToken,
        slack_app_token: SlackAppToken,
    ):
        ctx.obj = create_slack_context(
            bot_token=slack_bot_token,
            app_token=slack_app_token,
        )

    @app.command()
    def send(ctx: typer.Context, channel: str, message: str):
        slack_ctx: SlackContext = ctx.obj
        slack_ctx.web_client.chat_postMessage(channel=channel, text=message)
    ```
"""

import logging
from dataclasses import dataclass
from typing import Annotated, Optional

import typer

logger = logging.getLogger(__name__)


# Type aliases for CLI parameters
SlackBotToken = Annotated[str, typer.Option(..., envvar="SLACK_BOT_TOKEN")]
SlackAppToken = Annotated[str, typer.Option(..., envvar="SLACK_APP_TOKEN")]


@dataclass
class SlackContext:
    """Typed Slack context with bot and optional app tokens.
    
    Attributes:
        bot_token: Slack Bot Token for Web API calls (posting messages, opening modals)
        app_token: Optional Slack App Token for Socket Mode (real-time events)
        web_client: Lazy-initialized WebClient (set by client initializer)
    """

    bot_token: str
    app_token: Optional[str] = None
    web_client: Optional[object] = None  # slack_sdk.WebClient, but avoid import


def create_slack_context(
    bot_token: SlackBotToken,
    app_token: Optional[SlackAppToken] = None,
    web_client_initializer: Optional[callable] = None,
) -> SlackContext:
    """Create Slack context with bot and optional app tokens.
    
    Args:
        bot_token: Slack Bot Token for Web API calls
        app_token: Optional Slack App Token for Socket Mode
        web_client_initializer: Optional function to initialize web client
            Should accept bot_token and return initialized client
    
    Returns:
        SlackContext with configured tokens
        
    Example:
        >>> def init_client(token):
        ...     from slack_sdk import WebClient
        ...     return WebClient(token=token)
        >>> ctx = create_slack_context(
        ...     bot_token="xoxb-...",
        ...     app_token="xapp-...",
        ...     web_client_initializer=init_client,
        ... )
    """
    logger.debug("Creating Slack context")

    web_client = None
    if web_client_initializer:
        logger.debug("Initializing Slack web client")
        web_client = web_client_initializer(bot_token)

    logger.debug("Slack context created successfully")

    return SlackContext(
        bot_token=bot_token,
        app_token=app_token,
        web_client=web_client,
    )


def create_slack_web_client_only(
    bot_token: SlackBotToken,
    web_client_initializer: Optional[callable] = None,
) -> SlackContext:
    """Create Slack context with only bot token (no Socket Mode).
    
    Useful for services that only need Web API access and don't need
    real-time Socket Mode events.
    
    Args:
        bot_token: Slack Bot Token for Web API calls
        web_client_initializer: Optional function to initialize web client
    
    Returns:
        SlackContext with bot token only
        
    Example:
        >>> ctx = create_slack_web_client_only(bot_token="xoxb-...")
    """
    return create_slack_context(
        bot_token=bot_token,
        app_token=None,
        web_client_initializer=web_client_initializer,
    )
