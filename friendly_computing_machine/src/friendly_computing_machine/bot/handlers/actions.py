import logging
import re

from slack_bolt import Ack

from external.manman.experience_api.models import StdinCommandRequest
from friendly_computing_machine.src.friendly_computing_machine.bot.app import app
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_client import (
    SlackWebClientFCM,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_enum import (
    SlackActionRegistry,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_payloads import (
    ActionPayload,
)
from friendly_computing_machine.src.friendly_computing_machine.manman.api import (
    ManManExperienceAPI,
)

logger = logging.getLogger(__name__)


@app.action("start_server")
def handle_start_server(ack: Ack, body, client: SlackWebClientFCM, logger):
    ack()
    payload = ActionPayload.from_dict(body)
    logger.info("Start server clicked")
    # Use the ManManAPI class to get the client
    try:
        mapi = ManManExperienceAPI.get_api()
        game_server_config_id = int(payload.action_body)
        mapi.start_game_server_gameserver_id_start_post(game_server_config_id)
        logger.info("started %s", game_server_config_id)
    except ValueError as e:
        logger.error(f"Failed to get ManMan API client: {e}")
        # Optionally inform the user about the configuration issue
        client.chat_postMessage(
            channel=payload.user_id, text="Error: ManMan API is not configured."
        )


@app.action("stop_server")
def handle_stop_server(ack: Ack, body, client: SlackWebClientFCM, logger):
    ack()
    logger.debug(body)
    payload = ActionPayload.from_dict(body)
    logger.info("Stop server clicked")
    try:
        mapi = ManManExperienceAPI.get_api()
        game_server_config_id = int(payload.action_body)
        mapi.stop_game_server_gameserver_id_stop_post(game_server_config_id)
    except Exception as e:
        logger.exception(f"Failed to get ManMan API client: {e}")
        # Optionally inform the user about the configuration issue
        client.chat_postMessage(
            channel=payload.user_id, text="Error: ManMan API is not configured."
        )


@app.action("restart_server")
def handle_restart_server(ack: Ack, body, client: SlackWebClientFCM, logger):
    ack()
    payload = ActionPayload.from_dict(body)
    logger.info("Restart server clicked")
    client.chat_postMessage(
        channel=payload.user_id,
        text="[Stub] Restart server action triggered. Currently does nothing.",
    )


@app.action("send_stdin_custom_command")
def handle_custom_command(ack: Ack, body, client: SlackWebClientFCM, logger):
    payload = ActionPayload.from_dict(body)
    # TODO - improve this
    raw_string = payload.action_body
    id, command = raw_string.split(";", 1)
    # TODO - pass in command_args as list instead of a single string
    mmexapi = ManManExperienceAPI.get_api()
    # TODO  does work?
    if not command:
        logger.error("No command provided in stdin action")
        ack(text="No command provided")
        return
    mmexapi.stdin_game_server_gameserver_id_stdin_post(
        int(id),
        StdinCommandRequest(commands=[command]),
    )
    ack()


@app.action(SlackActionRegistry.MANMAN_WORKER_STOP)
def handle_manman_worker_stop(
    ack: Ack, body, client: SlackWebClientFCM, logger: logging.Logger
):
    logger.info("ManMan worker stop action triggered")
    mmexapi = ManManExperienceAPI.get_api()
    # This is kind of risky because it shuts down the current worker
    # but it is the only way to stop a worker in ManMan Experience atm
    # eventually we should have a proper stop endpoint with ID to avoid this weirdness
    mmexapi.worker_shutdown_worker_shutdown_post()
    ack()


@app.action(SlackActionRegistry.MANMAN_SERVER_CREATE)
def handle_manman_server_create(
    ack: Ack, body, client: SlackWebClientFCM, logger: logging.Logger
):
    logger.info("ManMan server create action triggered")
    payload = ActionPayload.from_dict(body)
    # not ideal, but we need to get the server ID from the private metadata
    # eventually this should be custom dataclasses and the such
    server_id = int(payload.action_body)
    mmexapi = ManManExperienceAPI.get_api()
    mmexapi.start_game_server_gameserver_id_start_post(server_id)
    ack()


@app.action(re.compile(f"^{SlackActionRegistry.MANMAN_SERVER_STDIN}_"))
def handle_manman_server_stdin(
    ack: Ack, body, client: SlackWebClientFCM, logger: logging.Logger
):
    logger.info("ManMan server stdin action triggered")

    payload = ActionPayload.from_dict(body)
    # TODO - improve this
    raw_string = payload.action_body
    id, command = raw_string.split(";", 1)
    # TODO - pass in command_args as list instead of a single string
    mmexapi = ManManExperienceAPI.get_api()
    # TODO  does work?
    mmexapi.stdin_game_server_gameserver_id_stdin_post(
        int(id),
        StdinCommandRequest(commands=[command]),
    )
    ack()


@app.action(SlackActionRegistry.MANMAN_SERVER_STOP)
def handle_manman_server_stop(
    ack: Ack, body, client: SlackWebClientFCM, logger: logging.Logger
):
    payload = ActionPayload.from_dict(body)
    logger.info("ManMan server stop action triggered")
    # not ideal, but we need to get the server ID from the private metadata
    # eventually this should be custom dataclasses and the such
    server_id = int(payload.action_body)
    mmexapi = ManManExperienceAPI.get_api()
    mmexapi.stop_game_server_gameserver_id_stop_post(server_id)
    ack(":( bye bye")

    # TODO 6/10/25
    # something is wrong with the stop. It is being passed the right id I think
    # but it's not doing anything on the server.
    # need more debug information in the server in this csae
    # also need to fix rabbitmq timeout issue or whatever that keeps happening in local terminal
    # also need proper worker and server instance ids chained to actios
    # and base classes for passing that metadata around
