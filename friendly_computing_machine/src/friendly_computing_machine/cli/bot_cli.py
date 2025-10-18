import logging
from dataclasses import dataclass
from typing import Optional

import typer

from libs.python.cli.providers.logging import EnableOTLP, create_logging_context
from libs.python.cli.providers.postgres import DatabaseContext, PostgresUrl
from libs.python.cli.providers.slack import (
    SlackAppToken,
    SlackBotToken,
    SlackContext,
)
from libs.python.cli.providers.combinators import (
    setup_postgres_with_fcm_init,
    setup_slack_with_fcm_init,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.app_env import (
    T_app_env,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.gemini import (
    T_google_api_key,
    setup_gemini,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.manman_host import (
    T_manman_host_url,
    setup_manman_experience_api,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.temporal import (
    T_temporal_host,
    setup_temporal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    should_run_migration,
)

logger = logging.getLogger(__name__)


@dataclass
class FCMBotContext:
    """Typed context for FCM bot CLI."""

    db: Optional[DatabaseContext] = None
    slack: Optional[SlackContext] = None
    # Legacy context dict for gradual migration
    legacy: dict = None


app = typer.Typer()


@app.callback()
def callback(
    ctx: typer.Context,
    slack_app_token: SlackAppToken,
    slack_bot_token: SlackBotToken,
    temporal_host: T_temporal_host,
    app_env: T_app_env,
    manman_host_url: T_manman_host_url,
    log_otlp: EnableOTLP = False,
):
    logger.debug("CLI callback starting")
    
    # Create logging context using new provider
    create_logging_context(
        service_name="friendly-computing-machine-bot",
        log_level="DEBUG",
        enable_otlp=log_otlp,
    )
    
    # Create Slack context with automatic FCM initialization
    slack_ctx = setup_slack_with_fcm_init(
        bot_token=slack_bot_token,
        app_token=slack_app_token,
    )
    
    # Create legacy context dict for remaining non-migrated dependencies
    legacy_ctx = {}
    setup_temporal(
        type("Context", (), {"obj": legacy_ctx})(),
        temporal_host,
        app_env,
    )
    setup_manman_experience_api(
        type("Context", (), {"obj": legacy_ctx})(),
        manman_host_url,
    )
    setup_gemini(
        type("Context", (), {"obj": legacy_ctx})(),
        "",  # Will be provided by commands that need it
    )
    
    # Store typed context
    ctx.obj = FCMBotContext(
        slack=slack_ctx,
        legacy=legacy_ctx,
    )
    
    logger.debug("CLI callback complete")


@app.command("run-taskpool")
def cli_run_taskpool(
    ctx: typer.Context,
    database_url: PostgresUrl,
    skip_migration_check: bool = False,
):
    fcm_ctx: FCMBotContext = ctx.obj
    
    # Create database context with automatic FCM initialization
    fcm_ctx.db = setup_postgres_with_fcm_init(database_url)
    
    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(fcm_ctx.db.engine, fcm_ctx.db.alembic_config):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed, starting normally")

    logger.info("starting task pool service")
    # Lazy import to avoid initializing dependencies during CLI parsing
    from friendly_computing_machine.src.friendly_computing_machine.bot.main import (
        run_taskpool_only,
    )

    run_taskpool_only()


@app.command("run-slack-socket-app")
def cli_run_slack_socket_app(
    ctx: typer.Context,
    google_api_key: T_google_api_key,
    database_url: PostgresUrl,
    skip_migration_check: bool = False,
):
    fcm_ctx: FCMBotContext = ctx.obj
    
    if skip_migration_check:
        logger.info("skipping migration check")
    else:
        logger.info("migration check passed, starting normally")

    # Setup gemini using legacy context
    setup_gemini(
        type("Context", (), {"obj": fcm_ctx.legacy})(),
        google_api_key,
    )
    
    # Create database context with automatic FCM initialization
    fcm_ctx.db = setup_postgres_with_fcm_init(database_url)

    logger.info("starting slack bot service (no task pool)")
    # Lazy import to avoid initializing Slack app during CLI parsing
    from friendly_computing_machine.src.friendly_computing_machine.bot.main import (
        run_slack_bot_only,
    )

    run_slack_bot_only(
        app_token=fcm_ctx.slack.app_token,
    )


@app.command("send-test-command")
def cli_bot_test_message(ctx: typer.Context, channel: str, message: str):
    fcm_ctx: FCMBotContext = ctx.obj
    
    # Lazy import to avoid initializing Slack app during CLI parsing
    from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
        slack_send_message,
    )

    slack_send_message(channel, message=message)


@app.command("who-am-i")
def cli_bot_who_am_i(ctx: typer.Context):
    fcm_ctx: FCMBotContext = ctx.obj
    
    # Lazy import to avoid initializing Slack app during CLI parsing
    from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
        slack_bot_who_am_i,
    )

    logger.info(slack_bot_who_am_i())
