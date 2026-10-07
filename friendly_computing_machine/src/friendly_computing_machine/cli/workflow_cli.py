import asyncio
import logging
from typing import Annotated

import typer

from libs.python.cli.params import (
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
from libs.python.cli.providers.postgres import PostgresUrl, create_postgres_context
from libs.python.cli.providers.slack import SlackBotToken

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    init_web_client,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    should_run_migration,
)
from friendly_computing_machine.src.friendly_computing_machine.gemini.client import (
    init_gemini_client,
)
from friendly_computing_machine.src.friendly_computing_machine.health import (
    run_health_server,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.control import (
    trigger_brain_job_async,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    get_temporal_client_async,
    get_temporal_queue_name,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.worker import (
    run_worker,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    init_temporal,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    init_whagent_client,
)

logger = logging.getLogger(__name__)

app = typer.Typer(
    context_settings={"obj": {}},
)


@app.callback()
@temporal_params
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
    logger.debug("CLI callback starting")

    # Get contexts from decorators
    temporal_config = ctx.obj.get('temporal', {})
    app_env = ctx.obj.get('app_env')

    # Initialize Temporal client
    init_temporal(host=temporal_config['host'], app_env=app_env)

    # Initialize the whagent-net client (service-account auth) -- the
    # whagent activities run in this worker process, not the Slack bot's.
    init_whagent_client(
        api_url=whagent_api_url,
        keycloak_token_url=whagent_keycloak_token_url,
        client_id=whagent_client_id,
        client_secret=whagent_client_secret,
        ui_public_url=whagent_ui_public_url,
    )

    # Store context
    ctx.obj['temporal_host'] = temporal_config['host']

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

    # Setup Gemini API
    gemini_config = ctx.obj.get('gemini', {})
    init_gemini_client(api_key=gemini_config['api_key'])
    
    # Setup Slack client
    init_web_client(slack_bot_token)
    
    # Start health server
    run_health_server()

    logger.info("starting temporal worker")
    asyncio.run(run_worker(app_env=ctx.obj['app_env']))


@app.command("brain-trigger")
def cli_brain_trigger(
    ctx: typer.Context,
    persona_id: int,
    job_kind: Annotated[str, typer.Argument(help="harvest, reflect, write, or snapshot")],
):
    """Run one Shitposter brain job now, through the same per-persona lock as scheduled runs."""

    async def _run():
        client = await get_temporal_client_async()
        return await trigger_brain_job_async(
            client,
            get_temporal_queue_name("main"),
            persona_id,
            job_kind,
            ctx.obj.get("app_env"),
        )

    result = asyncio.run(_run())
    print(f"run_id={result.run_id} status={result.status}")
    if result.skip_reason:
        print(f"skip_reason={result.skip_reason}")


@app.command("test")
def cli_bot_test_message():
    print("hello world")
