from manman.models import GameServerConfig, GameServerInstance, ManManBase, Worker


class StdinCommandRequest(ManManBase):
    """
    Request to send to the worker to start a game server instance.
    """

    commands: list[str]


class CurrentInstanceResponse(ManManBase):
    """
    Response to the worker to start a game server instance.
    """

    game_server_instances: list[GameServerInstance]
    workers: list[Worker]
    configs: list[GameServerConfig]

    @classmethod
    def from_instances(
        cls, instances: list[GameServerInstance]
    ) -> "CurrentInstanceResponse":
        workers = {instance.worker_id: instance.worker for instance in instances}
        configs = {
            instance.game_server_config_id: instance.game_server_config
            for instance in instances
        }
        return cls(
            game_server_instances=instances,
            workers=list(workers.values()),
            configs=list(configs.values()),
        )
