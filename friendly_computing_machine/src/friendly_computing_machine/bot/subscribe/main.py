import logging

from libs.python.rmq import get_rabbitmq_connection
from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    get_slack_web_client,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.service import (
    ManManSubscribeService,
)
from friendly_computing_machine.src.friendly_computing_machine.manman.api import (
    ManManStatusAPI,
)

logger = logging.getLogger(__name__)


def run_manman_subscribe(app_env: str):
    """
    Run the ManMan Subscribe Service.

    This starts the service that listens to RabbitMQ topics for manman events
    and sends notifications to Slack.

    Args:
        app_env: Application environment string
    """
    logger.info("Starting ManMan Subscribe Service")

    rabbit_mq_connection = get_rabbitmq_connection()
    slack_api = get_slack_web_client()
    manman_status_api = ManManStatusAPI.get_api()

    service = ManManSubscribeService(
        app_env, rabbit_mq_connection, slack_api, manman_status_api
    )
    try:
        service.start()
    except Exception as e:
        logger.error(
            f"Unhandled exception in service's main async task: {e}", exc_info=True
        )
        raise  # Re-raise to allow outer try/except to catch it
    finally:
        # This block executes on normal completion of _consumer_bg_task,
        # cancellation, or if an unhandled exception occurred in the try block.
        if "service" in locals() and service:  # Ensure service was instantiated
            logger.info(
                "Main async task is ending. Ensuring ManManSubscribeService is stopped."
            )
            service.stop()
