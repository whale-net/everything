import logging

from slack_bolt.adapter.socket_mode import SocketModeHandler

# Import handlers to register decorators with the app proxy
from friendly_computing_machine.bot import handlers  # noqa
from friendly_computing_machine.bot.app import get_slack_app
from friendly_computing_machine.bot.task.taskpool import create_default_taskpool
from friendly_computing_machine.health import run_health_server
from friendly_computing_machine.util import NamedThreadPool

logger = logging.getLogger(__name__)


def run_slack_bot_only(app_token: str):
    """Run only the Slack bot without the task pool."""
    logger.info("starting slack bot service (no task pool)")
    with NamedThreadPool() as executor:
        slack_socket_handler = SocketModeHandler(get_slack_app(), app_token)

        executor.submit(slack_socket_handler.start, thread_name="bolt")
        run_health_server()
        logger.info("slack bot service started without task pool")


def run_taskpool_only():
    """Run only the task pool without the Slack bot."""
    logger.info("starting task pool service")
    with NamedThreadPool() as executor:
        task_pool = create_default_taskpool()

        executor.submit(task_pool.start, thread_name="task")
        run_health_server()
        logger.info("task pool service started without slack bot")
