from pydantic import BaseModel

from manman.models import GameServer, GameServerConfig, GameServerInstance


class GameStartRequest(BaseModel):
    game_server: GameServer
    game_server_config: GameServerConfig
    game_server_instance: GameServerInstance
