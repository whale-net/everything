import logging

from slack_bolt import Ack

from friendly_computing_machine.bot.app import app
from friendly_computing_machine.bot.modal_builder import build_server_select_modal
from friendly_computing_machine.bot.slack_client import SlackWebClientFCM
from friendly_computing_machine.bot.slack_payloads import ShortcutPayload

logger = logging.getLogger(__name__)

# Removed unused DUMMY_SERVERS variable to clean up the code.


@app.shortcut("server_control_shortcut")
def open_server_select_modal(ack: Ack, body, client: SlackWebClientFCM, logger):
    # UNUSED?
    ack()
    try:
        payload = ShortcutPayload.from_dict(body)
        modal = build_server_select_modal()
        client.views_open(
            trigger_id=payload.trigger_id,
            view=modal.build(),
        )
    except Exception as e:
        logger.error(f"Error opening server select modal: {e}")
