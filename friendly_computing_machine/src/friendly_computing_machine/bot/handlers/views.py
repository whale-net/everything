import logging

from slack_bolt import Ack

from friendly_computing_machine.bot.app import SlackWebClientFCM, app
from friendly_computing_machine.bot.modal_schemas import ServerActionModal
from friendly_computing_machine.bot.slack_payloads import ViewSubmissionPayload
from friendly_computing_machine.manman.api import OldManManAPI

logger = logging.getLogger(__name__)


@app.view("server_select_modal")
def handle_server_select_submission(ack: Ack, body, client: SlackWebClientFCM, logger):
    ack()
    try:
        payload = ViewSubmissionPayload.from_dict(body)

        if payload.selected_server is None:
            logger.error("No server selected")
            return

        server_config_id = int(payload.selected_server)
        logger.info(f"Server selected: {server_config_id}")
        manman = OldManManAPI.get_api()
        # TODO - add endpoint for just config info by id
        configs = manman.get_game_servers_host_gameserver_get()
        config = next(
            (
                config
                for config in configs
                if config.game_server_config_id == server_config_id
            ),
            None,
        )
        if config is None:
            logger.error(f"Server config not found for ID: {server_config_id}")
            return

        modal = ServerActionModal(
            server_name=config.name,
        )
        client.views_open(
            trigger_id=payload.trigger_id,
            # TODO - move arg out of build and into modal
            view=modal.build(
                game_server_instance_id=int(payload.selected_server)
                if payload.selected_server.isdigit()
                else -1
            ),
        )
        logger.info(f"Server manager opened for: {config.name}")
    except Exception as e:
        logger.error(f"Error opening server action modal: {e}")


@app.view("server_action_modal")
def handle_view_submission_events(ack, body, logger):
    ack()
    logger.info(body)
