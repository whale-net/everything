import logging
from dataclasses import dataclass, field
from typing import Annotated, Optional

import typer

from libs.python.cli.params import logging_params
from libs.python.cli.providers.postgres import (
    DatabaseContext,
    PostgresUrl,
    create_postgres_context,
)
from libs.python.cli.providers.rabbitmq import (
    RabbitMQContext,
    create_rabbitmq_context,
)
from libs.python.cli.providers.slack import SlackContext, create_slack_context
from libs.python.cli.params import (
    rmq_params,
    slack_params,
    logging_params,
    AppEnv,
    ManManStatusApiUrl,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.main import (
    run_manman_subscribe,
)
from friendly_computing_machine.src.friendly_computing_machine.manman.api import (
    ManManStatusAPI,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    should_run_migration,
)
from friendly_computing_machine.src.friendly_computing_machine.health import (
    run_health_server,
)

logger = logging.getLogger(__name__)


@dataclass
class FCMSubscribeContext:
    """Typed context for FCM subscribe CLI."""

    db: Optional[DatabaseContext] = None
    rabbitmq: Optional[RabbitMQContext] = None
    slack: Optional[object] = None  # SlackContext
    app_env: str = ""
    manman_host_url: str = ""


app = typer.Typer()


@app.callback()
@rmq_params      # Injects 7 RabbitMQ parameters
@slack_params    # Injects 2 Slack parameters
@logging_params  # Auto-configures logging from environment variables
def callback(
    ctx: typer.Context,
    app_env: AppEnv,
    manman_status_api_url: ManManStatusApiUrl,
):
    """
    ManMan Subscribe Service - Event-driven microservice for manman notifications.

    Subscribes to RabbitMQ topics for worker and instance lifecycle events
    and sends formatted Slack notifications with action buttons.
    
    Note: Service parameters (RabbitMQ, Slack, Logging) are injected by decorators.
    """
    logger.debug("Subscribe CLI callback starting")
    
    # Create Slack context with FCM initialization
    from friendly_computing_machine.src.friendly_computing_machine.bot.app import init_web_client
    
    slack_config = ctx.obj.get('slack', {})
    slack_ctx = create_slack_context(
        bot_token=slack_config['bot_token'],
        web_client_initializer=init_web_client,
    )
    
    # Create RabbitMQ context from decorator-injected params
    rabbitmq_config = ctx.obj.get('rabbitmq', {})
    if rabbitmq_config:
        # Use standard lib RabbitMQ with proper initialization
        from libs.python.rmq import init_rabbitmq_from_config
        
        # Initialize RabbitMQ with the config
        init_rabbitmq_from_config(rabbitmq_config)
        
        rabbitmq_ctx = create_rabbitmq_context(**rabbitmq_config)
    else:
        rabbitmq_ctx = None
    
    # Initialize ManMan Status API with its dedicated URL
    status_url = manman_status_api_url.strip().rstrip("/")
    ManManStatusAPI.init(status_url)
    logger.info(f"ManMan Status API initialized with host: {status_url}")
    
    # Store typed context
    ctx.obj = FCMSubscribeContext(
        rabbitmq=rabbitmq_ctx,
        slack=slack_ctx,
        app_env=app_env,
        manman_host_url=status_url,
    )
    
    logger.debug("Subscribe CLI callback complete")


@app.command("run")
def cli_run(
    ctx: typer.Context,
    database_url: PostgresUrl,
    skip_migration_check: bool = False,
):
    """
    Start the ManMan Subscribe Service.

    This service subscribes to RabbitMQ topics for manman worker and instance events
    and sends formatted notifications to Slack with action buttons.
    """
    subscribe_ctx: FCMSubscribeContext = ctx.obj
    
    # Create database context with FCM initialization
    from friendly_computing_machine.src.friendly_computing_machine.db.util import init_engine
    
    subscribe_ctx.db = create_postgres_context(
        database_url=database_url,
        migrations_package="friendly_computing_machine.src.migrations",
        engine_initializer=init_engine,
    )

    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(subscribe_ctx.db.engine, subscribe_ctx.db.alembic_config):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed, starting normally")
    
    run_health_server()
    logger.info("starting manman subscribe service")
    
    # Run the subscribe service
    run_manman_subscribe(app_env=subscribe_ctx.app_env)
