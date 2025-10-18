import logging
from dataclasses import dataclass
from typing import Annotated, Optional

import google.generativeai as genai
import typer

from libs.python.cli.params import (
    slack_params,
    pg_params,
    logging_params,
)
from libs.python.cli.providers.logging import create_logging_context
from libs.python.cli.providers.postgres import DatabaseContext, PostgresUrl
from libs.python.cli.providers.slack import SlackContext
from libs.python.cli.providers.combinators import (
    setup_postgres_with_fcm_init,
    setup_slack_with_fcm_init,
)
from friendly_computing_machine.src.friendly_computing_machine.manman.api import (
    ManManExperienceAPI,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    init_temporal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    should_run_migration,
)

logger = logging.getLogger(__name__)

# Type aliases
T_temporal_host = Annotated[str, typer.Option(..., envvar="TEMPORAL_HOST")]
T_app_env = Annotated[str, typer.Option(..., envvar="APP_ENV")]
T_manman_host_url = Annotated[str, typer.Option(..., envvar="MANMAN_HOST_URL")]
T_google_api_key = Annotated[str, typer.Option(..., envvar="GOOGLE_API_KEY")]


@dataclass
class FCMBotContext:
    """Typed context for FCM bot CLI."""

    db: Optional[DatabaseContext] = None
    slack: Optional[SlackContext] = None
    temporal_host: str = ""
    app_env: str = ""
    manman_host_url: str = ""


app = typer.Typer()


@app.callback()
@slack_params    # Injects Slack parameters
@pg_params       # Injects PostgreSQL parameters
@logging_params  # Injects logging parameters
def callback(
    ctx: typer.Context,
    temporal_host: T_temporal_host,
    app_env: T_app_env,
    manman_host_url: T_manman_host_url,
):
    logger.debug("CLI callback starting")
    
    # Create logging context from decorator-injected params
    log_config = ctx.obj.get('logging', {})
    create_logging_context(
        service_name="friendly-computing-machine-bot",
        log_level="DEBUG",
        enable_otlp=log_config.get('enable_otlp', False),
    )
    
    # Create Slack context from decorator-injected params
    slack_config = ctx.obj.get('slack', {})
    slack_ctx = setup_slack_with_fcm_init(
        bot_token=slack_config['bot_token'],
        app_token=slack_config.get('app_token', ''),
    )
    
    # Initialize Temporal client
    init_temporal(host=temporal_host, app_env=app_env)
    
    # Initialize ManMan Experience API
    url = manman_host_url.strip().rstrip("/")
    ManManExperienceAPI.init(url + "/experience")
    logger.info(f"ManMan Experience API initialized with host: {url}")
    
    # Store typed context
    ctx.obj = FCMBotContext(
        slack=slack_ctx,
        temporal_host=temporal_host,
        app_env=app_env,
        manman_host_url=manman_host_url,
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

    # Setup Gemini API
    genai.configure(api_key=google_api_key)
    
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
