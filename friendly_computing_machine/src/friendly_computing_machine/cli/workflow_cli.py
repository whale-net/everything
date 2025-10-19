import asyncio
import logging
from typing import Annotated

from google import genai
import typer

from libs.python.cli.params import logging_params, temporal_params, gemini_params, AppEnv
from libs.python.cli.providers.logging import create_logging_context
from libs.python.cli.providers.postgres import PostgresUrl, create_postgres_context
from libs.python.cli.providers.slack import SlackBotToken

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

app = typer.Typer(
    context_settings={"obj": {}},
)


@app.callback()
@temporal_params
@logging_params
def callback(
    ctx: typer.Context,
    app_env: AppEnv,
):
    logger.debug("CLI callback starting")
    
    # Get contexts from decorators
    temporal_config = ctx.obj.get('temporal', {})
    log_config = ctx.obj.get('logging', {})
    
    # Setup logging
    create_logging_context(
        service_name="friendly-computing-machine-workflow",
        log_level="DEBUG",
        enable_otlp=log_config.get('enable_otlp', False),
    )
    
    # Initialize Temporal client
    init_temporal(host=temporal_config['host'], app_env=app_env)
    
    # Store context
    ctx.obj['temporal_host'] = temporal_config['host']
    ctx.obj['app_env'] = app_env
    
    logger.debug("CLI callback complete")


@app.command("run")
@gemini_params
def cli_run(
    ctx: typer.Context,
    database_url: PostgresUrl,
    slack_bot_token: SlackBotToken,
    skip_migration_check: bool = False,
):
    # Setup database with FCM initialization
    from friendly_computing_machine.src.friendly_computing_machine.db.util import init_engine
    
    db_ctx = create_postgres_context(
        database_url=database_url,
        migrations_package="friendly_computing_machine.src.migrations",
        engine_initializer=init_engine,
    )
    
    # Check migrations
    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(db_ctx.engine, db_ctx.alembic_config):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed, starting normally")

    # Setup Gemini API client (stored globally for activity access)
    gemini_config = ctx.obj.get('gemini', {})
    genai_client = genai.Client(api_key=gemini_config['api_key'])
    # Store globally for activities to access
    import friendly_computing_machine.src.friendly_computing_machine.temporal.ai.activity as ai_activity
    ai_activity.genai_client = genai_client
    
    # Setup Slack client
    init_web_client(slack_bot_token)
    
    # Start health server
    run_health_server()

    logger.info("starting temporal worker")
    asyncio.run(run_worker(app_env=ctx.obj['app_env']))


@app.command("test")
def cli_bot_test_message():
    print("hello world")
