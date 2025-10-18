import asyncio
import logging
from typing import Annotated

import google.generativeai as genai
import typer

from libs.python.cli.params import logging_params
from libs.python.cli.providers.logging import create_logging_context
from libs.python.cli.providers.postgres import PostgresUrl
from libs.python.cli.providers.slack import SlackBotToken
from libs.python.cli.providers.combinators import setup_postgres_with_fcm_init

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    init_web_client,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    should_run_migration,
)
from friendly_computing_machine.src.friendly_computing_machine.health import (
    run_health_server,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.worker import (
    run_worker,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    init_temporal,
)

logger = logging.getLogger(__name__)

# Type aliases
T_temporal_host = Annotated[str, typer.Option(..., envvar="TEMPORAL_HOST")]
T_app_env = Annotated[str, typer.Option(..., envvar="APP_ENV")]
T_google_api_key = Annotated[str, typer.Option(..., envvar="GOOGLE_API_KEY")]

app = typer.Typer(
    context_settings={"obj": {}},
)


@app.callback()
@logging_params
def callback(
    ctx: typer.Context,
    temporal_host: T_temporal_host,
    app_env: T_app_env,
):
    logger.debug("CLI callback starting")
    
    # Setup logging from decorator-injected params
    log_config = ctx.obj.get('logging', {})
    create_logging_context(
        service_name="friendly-computing-machine-workflow",
        log_level="DEBUG",
        enable_otlp=log_config.get('enable_otlp', False),
    )
    
    # Initialize Temporal client
    init_temporal(host=temporal_host, app_env=app_env)
    
    # Store context
    ctx.obj['temporal_host'] = temporal_host
    ctx.obj['app_env'] = app_env
    
    logger.debug("CLI callback complete")


@app.command("run")
def cli_run(
    ctx: typer.Context,
    google_api_key: T_google_api_key,
    database_url: PostgresUrl,
    slack_bot_token: SlackBotToken,
    skip_migration_check: bool = False,
):
    # Setup database
    db_ctx = setup_postgres_with_fcm_init(database_url)
    
    # Check migrations
    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(db_ctx.engine, db_ctx.alembic_config):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed, starting normally")

    # Setup Gemini API
    genai.configure(api_key=google_api_key)
    
    # Setup Slack client
    init_web_client(slack_bot_token)
    
    # Start health server
    run_health_server()

    logger.info("starting temporal worker")
    asyncio.run(run_worker(app_env=ctx.obj['app_env']))


@app.command("test")
def cli_bot_test_message():
    print("hello world")
