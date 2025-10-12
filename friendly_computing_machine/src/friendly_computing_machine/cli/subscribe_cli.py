import logging

import typer

from friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.main import (
    run_manman_subscribe,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.app_env import (
    FILENAME as APP_ENV_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.app_env import (
    T_app_env,
    setup_app_env,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.db import (
    FILENAME as DB_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.db import (
    T_database_url,
    setup_db,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.log import (
    setup_logging,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.manman_host import (
    T_manman_host_url,
    setup_manman_status_api,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.rabbitmq import (
    T_rabbitmq_enable_ssl,
    T_rabbitmq_host,
    T_rabbitmq_password,
    T_rabbitmq_port,
    T_rabbitmq_ssl_hostname,
    T_rabbitmq_user,
    T_rabbitmq_vhost,
    setup_rabbitmq,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.slack import (
    T_slack_bot_token,
    setup_slack_web_client_only,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    should_run_migration,
)
from friendly_computing_machine.src.friendly_computing_machine.health import (
    run_health_server,
)

logger = logging.getLogger(__name__)

app = typer.Typer(
    context_settings={"obj": {}},
)


@app.callback()
def callback(
    ctx: typer.Context,
    slack_bot_token: T_slack_bot_token,
    app_env: T_app_env,
    manman_host_url: T_manman_host_url,
    rabbitmq_host: T_rabbitmq_host,
    rabbitmq_port: T_rabbitmq_port = 5672,
    rabbitmq_user: T_rabbitmq_user = None,
    rabbitmq_password: T_rabbitmq_password = None,
    rabbitmq_enable_ssl: T_rabbitmq_enable_ssl = False,
    rabbitmq_ssl_hostname: T_rabbitmq_ssl_hostname = None,
    rabbitmq_vhost: T_rabbitmq_vhost = "/",
    log_otlp: bool = False,
):
    """
    ManMan Subscribe Service - Event-driven microservice for manman notifications.

    Subscribes to RabbitMQ topics for worker and instance lifecycle events
    and sends formatted Slack notifications with action buttons.
    """
    logger.debug("Subscribe CLI callback starting")
    setup_logging(ctx, log_otlp=log_otlp)
    setup_app_env(ctx, app_env)
    setup_slack_web_client_only(ctx, slack_bot_token)
    setup_manman_status_api(ctx, manman_host_url)
    setup_rabbitmq(
        ctx,
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        rabbitmq_user=rabbitmq_user,
        rabbitmq_password=rabbitmq_password,
        rabbitmq_enable_ssl=rabbitmq_enable_ssl,
        rabbitmq_ssl_hostname=rabbitmq_ssl_hostname,
        rabbitmq_vhost=rabbitmq_vhost,
    )
    logger.debug("Subscribe CLI callback complete")


@app.command("run")
def cli_run(
    ctx: typer.Context,
    database_url: T_database_url,
    skip_migration_check: bool = False,
):
    """
    Start the ManMan Subscribe Service.

    This service subscribes to RabbitMQ topics for manman worker and instance events
    and sends formatted notifications to Slack with action buttons.
    """
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
    run_health_server()
    logger.info("starting manman subscribe service")
    run_manman_subscribe(app_env=ctx.obj[APP_ENV_FILENAME]["app_env"])
