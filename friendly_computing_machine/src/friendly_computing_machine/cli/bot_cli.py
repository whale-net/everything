import logging

import typer

from friendly_computing_machine.cli.context.app_env import T_app_env
from friendly_computing_machine.cli.context.db import FILENAME as DB_FILENAME
from friendly_computing_machine.cli.context.db import T_database_url, setup_db
from friendly_computing_machine.cli.context.gemini import T_google_api_key, setup_gemini
from friendly_computing_machine.cli.context.log import setup_logging
from friendly_computing_machine.cli.context.manman_host import (
    T_manman_host_url,
    setup_manman_experience_api,
    setup_old_manman_api,
)
from friendly_computing_machine.cli.context.slack import FILENAME as SLACK_FILENAME
from friendly_computing_machine.cli.context.slack import (
    T_slack_app_token,
    T_slack_bot_token,
    setup_slack,
)
from friendly_computing_machine.cli.context.temporal import (
    T_temporal_host,
    setup_temporal,
)
from friendly_computing_machine.db.util import should_run_migration

logger = logging.getLogger(__name__)
app = typer.Typer(
    context_settings={"obj": {}},
)


@app.callback()
def callback(
    ctx: typer.Context,
    slack_app_token: T_slack_app_token,
    slack_bot_token: T_slack_bot_token,
    temporal_host: T_temporal_host,
    app_env: T_app_env,
    manman_host_url: T_manman_host_url,
    log_otlp: bool = False,
):
    logger.debug("CLI callback starting")
    setup_logging(ctx, log_otlp=log_otlp)
    setup_slack(ctx, slack_app_token, slack_bot_token)
    setup_temporal(ctx, temporal_host, app_env)
    setup_manman_experience_api(ctx, manman_host_url)
    setup_old_manman_api(ctx, manman_host_url)
    logger.debug("CLI callback complete")


@app.command("run-taskpool")
def cli_run_taskpool(
    ctx: typer.Context,
    database_url: T_database_url,
    skip_migration_check: bool = False,
):
    setup_db(ctx, database_url)
    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(
        ctx.obj[DB_FILENAME].engine, ctx.obj[DB_FILENAME].alembic_config
    ):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed, starting normally")

    logger.info("starting task pool service")
    # Lazy import to avoid initializing dependencies during CLI parsing
    from friendly_computing_machine.bot.main import run_taskpool_only

    run_taskpool_only()


@app.command("run-slack-socket-app")
def cli_run_slack_socket_app(
    ctx: typer.Context,
    google_api_key: T_google_api_key,
    database_url: T_database_url,
    skip_migration_check: bool = False,
):
    if skip_migration_check:
        logger.info("skipping migration check")
    else:
        logger.info("migration check passed, starting normally")

    setup_gemini(ctx, google_api_key)
    # TODO - one day this could be moved to temporal jobs
    # which would remove the need for this db check and allow multiple socket apps
    # very cool
    setup_db(ctx, database_url)

    logger.info("starting slack bot service (no task pool)")
    # Lazy import to avoid initializing Slack app during CLI parsing
    from friendly_computing_machine.bot.main import run_slack_bot_only

    run_slack_bot_only(
        app_token=ctx.obj[SLACK_FILENAME]["slack_app_token"],
    )


@app.command("send-test-command")
def cli_bot_test_message(channel: str, message: str):
    # Lazy import to avoid initializing Slack app during CLI parsing
    from friendly_computing_machine.bot.util import slack_send_message

    slack_send_message(channel, message=message)


@app.command("who-am-i")
def cli_bot_who_am_i():
    # Lazy import to avoid initializing Slack app during CLI parsing
    from friendly_computing_machine.bot.util import slack_bot_who_am_i

    logger.info(slack_bot_who_am_i())
