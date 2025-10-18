import logging
from dataclasses import dataclass, field
from typing import Optional

import typer

from libs.python.cli.providers.logging import EnableOTLP, create_logging_context
from libs.python.cli.providers.postgres import DatabaseContext, PostgresUrl
from libs.python.cli.providers.slack import SlackBotToken
from libs.python.cli.providers.rabbitmq import RabbitMQContext, create_rabbitmq_context
from libs.python.cli.providers.combinators import (
    setup_postgres_with_fcm_init,
    setup_slack_with_fcm_init,
)
from libs.python.cli.params import (
    rmq_params,
    slack_params,
    logging_params,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.main import (
    run_manman_subscribe,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.app_env import (
    T_app_env,
    setup_app_env,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.manman_host import (
    T_manman_host_url,
    setup_manman_status_api,
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
    # Legacy context dict for gradual migration
    legacy: dict = field(default_factory=dict)


app = typer.Typer()


@app.callback()
@rmq_params      # Injects 7 RabbitMQ parameters
@slack_params    # Injects 2 Slack parameters  
@logging_params  # Injects 1 logging parameter
def callback(
    ctx: typer.Context,
    app_env: T_app_env,
    manman_host_url: T_manman_host_url,
):
    """
    ManMan Subscribe Service - Event-driven microservice for manman notifications.

    Subscribes to RabbitMQ topics for worker and instance lifecycle events
    and sends formatted Slack notifications with action buttons.
    
    Note: Service parameters (RabbitMQ, Slack, Logging) are injected by decorators.
    """
    logger.debug("Subscribe CLI callback starting")
    
    # Create logging context from decorator-injected params
    log_config = ctx.obj.get('logging', {})
    create_logging_context(
        service_name="friendly-computing-machine-subscribe",
        log_level="DEBUG",
        enable_otlp=log_config.get('enable_otlp', False),
    )
    
    # Create Slack context from decorator-injected params
    slack_config = ctx.obj.get('slack', {})
    slack_ctx = setup_slack_with_fcm_init(bot_token=slack_config['bot_token'])
    
    # Create RabbitMQ context from decorator-injected params
    rabbitmq_config = ctx.obj.get('rabbitmq', {})
    if rabbitmq_config:
        rabbitmq_ctx = create_rabbitmq_context(**rabbitmq_config)
    else:
        rabbitmq_ctx = None
    
    # Create legacy context dict for remaining non-migrated dependencies
    legacy_ctx = {}
    setup_app_env(
        type("Context", (), {"obj": legacy_ctx})(),
        app_env,
    )
    setup_manman_status_api(
        type("Context", (), {"obj": legacy_ctx})(),
        manman_host_url,
    )
    
    # Store typed context
    ctx.obj = FCMSubscribeContext(
        rabbitmq=rabbitmq_ctx,
        slack=slack_ctx,
        legacy=legacy_ctx,
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
    
    # Create database context with automatic FCM initialization
    subscribe_ctx.db = setup_postgres_with_fcm_init(database_url)

    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(subscribe_ctx.db.engine, subscribe_ctx.db.alembic_config):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed, starting normally")
    
    run_health_server()
    logger.info("starting manman subscribe service")
    
    # Get app_env from legacy context
    from friendly_computing_machine.src.friendly_computing_machine.cli.context.app_env import (
        FILENAME as APP_ENV_FILENAME,
    )
    run_manman_subscribe(app_env=subscribe_ctx.legacy[APP_ENV_FILENAME]["app_env"])
