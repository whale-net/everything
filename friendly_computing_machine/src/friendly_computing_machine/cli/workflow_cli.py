import asyncio
import logging

import typer

from friendly_computing_machine.cli.context.app_env import FILENAME as APP_ENV_FILENAME
from friendly_computing_machine.cli.context.app_env import T_app_env, setup_app_env
from friendly_computing_machine.cli.context.db import FILENAME as DB_FILENAME
from friendly_computing_machine.cli.context.db import T_database_url, setup_db
from friendly_computing_machine.cli.context.gemini import T_google_api_key, setup_gemini
from friendly_computing_machine.cli.context.log import setup_logging

# from friendly_computing_machine.cli.context.slack import (
#     setup_slack,
#     T_slack_app_token,
#     FILENAME as SLACK_FILENAME,
# )
from friendly_computing_machine.cli.context.slack import (
    T_slack_bot_token,
    setup_slack_web_client_only,
)
from friendly_computing_machine.cli.context.temporal import (
    T_temporal_host,
    setup_temporal,
)
from friendly_computing_machine.db.util import should_run_migration
from friendly_computing_machine.health import run_health_server
from friendly_computing_machine.temporal.worker import run_worker

logger = logging.getLogger(__name__)

app = typer.Typer(
    context_settings={"obj": {}},
)


@app.callback()
def callback(
    ctx: typer.Context,
    # slack_bot_token: Annotated[str, typer.Option(envvar="SLACK_BOT_TOKEN")],
    # slack_app_token: T_slack_app_token,
    temporal_host: T_temporal_host,
    app_env: T_app_env,
    log_otlp: bool = False,
):
    logger.debug("CLI callback starting")
    setup_logging(ctx, log_otlp=log_otlp)
    # setup_slack(ctx, slack_app_token)
    setup_app_env(ctx, app_env)
    setup_temporal(ctx, temporal_host, app_env)
    logger.debug("CLI callback complete")


@app.command("run")
def cli_run(
    ctx: typer.Context,
    # keeping these on run for now just since it seems right
    google_api_key: T_google_api_key,
    database_url: T_database_url,
    slack_bot_token: T_slack_bot_token,
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

    setup_gemini(ctx, google_api_key)
    setup_slack_web_client_only(ctx, slack_bot_token)
    run_health_server()

    logger.info("starting temporal worker")
    # TODO - pass down context
    asyncio.run(run_worker(app_env=ctx.obj[APP_ENV_FILENAME]["app_env"]))


@app.command("test")
def cli_bot_test_message():
    print("hello world")
