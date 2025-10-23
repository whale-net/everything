import logging
from typing import Annotated, Optional

import google.generativeai as genai
import typer

from libs.python.cli.params import (
    slack_params,
    pg_params,
    temporal_params,
    gemini_params,
    logging_params,
    AppEnv,
    ManManExperienceApiUrl,
)
from libs.python.logging import configure_logging
from libs.python.cli.providers.postgres import (
    DatabaseContext,
    PostgresUrl,
    create_postgres_context,
)
from libs.python.cli.providers.slack import SlackContext, create_slack_context
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

app = typer.Typer()


@app.callback()
@logging_params
def callback(ctx: typer.Context, app_env: AppEnv):
    # Configure OTLP-first logging (with CLI flag override)
    log_config = ctx.obj.get("logging", {})
    configure_logging(
        service_name="friendly-computing-machine-bot",
        service_version="1.0.0",
        deployment_environment=app_env,
        log_level="DEBUG",
        enable_otlp=log_config.get("enable_otlp", True),  # Default True, CLI can override
        json_format=False,
    )
    
    # Get contexts from decorators
    temporal_config = ctx.obj.get('temporal', {})
    gemini_config = ctx.obj.get('gemini', {})
    slack_config = ctx.obj.get('slack', {})
    
    # Create Slack context with FCM initialization
    from friendly_computing_machine.src.friendly_computing_machine.bot.app import init_web_client
    
    slack_ctx = create_slack_context(
        bot_token=slack_config['bot_token'],
        app_token=slack_config.get('app_token', ''),
        web_client_initializer=init_web_client,
    )
    
    # Initialize Temporal client
    init_temporal(host=temporal_config['host'], app_env=app_env)
    
    # Initialize Gemini
    genai.configure(api_key=gemini_config['api_key'])
    
    # Initialize ManMan Experience API with its dedicated URL
    experience_url = manman_experience_api_url.strip().rstrip("/")
    ManManExperienceAPI.init(experience_url)
    logger.info(f"ManMan Experience API initialized with host: {experience_url}")
    
    # Store context in dict (keep compatible with decorator pattern)
    ctx.obj['slack'] = slack_ctx
    ctx.obj['temporal_host'] = temporal_config['host']
    ctx.obj['app_env'] = app_env
    ctx.obj['manman_experience_api_url'] = experience_url
    
    logger.debug("CLI callback complete")


@app.command("run-taskpool")
def cli_run_taskpool(
    ctx: typer.Context,
    database_url: PostgresUrl,
    skip_migration_check: bool = False,
):
    # Create database context with FCM initialization
    from friendly_computing_machine.src.friendly_computing_machine.db.util import init_engine
    
    db_ctx = create_postgres_context(
        database_url=database_url,
        migrations_package="friendly_computing_machine.src.migrations",
        engine_initializer=init_engine,
    )
    
    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(db_ctx.engine, db_ctx.alembic_config):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed, starting normally")

    logger.info("starting task pool service")
    # Lazy import to avoid initializing dependencies during module import
    from friendly_computing_machine.src.friendly_computing_machine.bot.main import (
        run_taskpool_only,
    )

    run_taskpool_only()


@app.command("run-slack-socket-app")
def cli_run_slack_socket_app(
    ctx: typer.Context,
    database_url: PostgresUrl,
    skip_migration_check: bool = False,
):
    if skip_migration_check:
        logger.info("skipping migration check")
    else:
        logger.info("migration check passed, starting normally")

    # Gemini API is already configured in callback
    
    # Create database context with FCM initialization
    from friendly_computing_machine.src.friendly_computing_machine.db.util import init_engine
    
    db_ctx = create_postgres_context(
        database_url=database_url,
        migrations_package="friendly_computing_machine.src.migrations",
        engine_initializer=init_engine,
    )

    logger.info("starting slack bot service (no task pool)")
    # Lazy import to avoid initializing Slack app during module import
    from friendly_computing_machine.src.friendly_computing_machine.bot.main import (
        run_slack_bot_only,
    )

    slack_ctx = ctx.obj['slack']
    run_slack_bot_only(
        app_token=slack_ctx.app_token,
    )


@app.command("send-test-command")
def cli_bot_test_message(ctx: typer.Context, channel: str, message: str):
    # Lazy import to avoid initializing Slack app during module import
    from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
        slack_send_message,
    )

    slack_send_message(channel, message=message)


@app.command("who-am-i")
def cli_bot_who_am_i(ctx: typer.Context):
    # Lazy import to avoid initializing Slack app during module import
    from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
        slack_bot_who_am_i,
    )

    logger.info(slack_bot_who_am_i())
