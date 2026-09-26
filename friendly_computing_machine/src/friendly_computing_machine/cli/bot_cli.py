import logging
from typing import Annotated, Optional

import typer

from libs.python.cli.params import (
    slack_params,
    pg_params,
    temporal_params,
    gemini_params,
    logging_params,
    WhagentApiUrl,
    WhagentUiPublicUrl,
    WhagentKeycloakTokenUrl,
    WhagentClientId,
    WhagentClientSecret,
)
from libs.python.cli.providers.app_env import app_env_params
from libs.python.cli.providers.postgres import (
    DatabaseContext,
    PostgresUrl,
    create_postgres_context,
)
from libs.python.cli.providers.slack import SlackContext, create_slack_context
from friendly_computing_machine.src.friendly_computing_machine.gemini.client import (
    init_gemini_client,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    init_whagent_client,
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
@temporal_params
@gemini_params
@slack_params
@logging_params  # Auto-configures logging from environment variables
@app_env_params  # Injects app_env from APP_ENV environment variable
def callback(
    ctx: typer.Context,
    whagent_api_url: WhagentApiUrl,
    whagent_ui_public_url: WhagentUiPublicUrl,
    whagent_keycloak_token_url: WhagentKeycloakTokenUrl,
    whagent_client_id: WhagentClientId,
    whagent_client_secret: WhagentClientSecret,
):
    # Get contexts from decorators
    temporal_config = ctx.obj.get('temporal', {})
    gemini_config = ctx.obj.get('gemini', {})
    slack_config = ctx.obj.get('slack', {})
    app_env = ctx.obj.get('app_env')
    
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
    init_gemini_client(api_key=gemini_config['api_key'])
    
    # Initialize the whagent-net client (service-account auth)
    init_whagent_client(
        api_url=whagent_api_url,
        keycloak_token_url=whagent_keycloak_token_url,
        client_id=whagent_client_id,
        client_secret=whagent_client_secret,
        ui_public_url=whagent_ui_public_url,
    )
    logger.info(f"whagent-net client initialized with host: {whagent_api_url}")

    # Store context in dict (keep compatible with decorator pattern)
    ctx.obj['slack'] = slack_ctx
    ctx.obj['temporal_host'] = temporal_config['host']
    ctx.obj['app_env'] = app_env
    
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
