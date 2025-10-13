from slack_sdk.models.blocks import Option

from friendly_computing_machine.src.friendly_computing_machine.bot.modal_schemas import (
    ServerSelectModal,
)
from friendly_computing_machine.src.friendly_computing_machine.manman.api import (
    ManManExperienceAPI,
)


def build_server_select_modal() -> ServerSelectModal:
    manman = ManManExperienceAPI.get_api()
    # active_server_response = (
    #     manman.get_active_game_server_instances_host_gameserver_instances_active_get()
    # )
    # servers: list[GameServerInstanceOutput] = (
    #     active_server_response.game_server_instances
    # )
    # configs: list[GameServerConfig] = active_server_response.configs
    # config_map = {config.game_server_config_id: config for config in configs}
    configs = manman.get_game_servers_gameserver_get()

    if len(configs) == 0:
        options = [
            Option(label="No servers available", value="no_servers_available"),
        ]
    else:
        options = [
            Option(
                label=config.name,
                value=str(config.game_server_config_id),
            )
            for config in configs
        ]
    modal = ServerSelectModal(options=options)
    return modal
