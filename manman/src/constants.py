"""
Constants and enums used throughout the manman application.
"""

from enum import StrEnum


class EntityRegistry(StrEnum):
    WORKER = "worker"
    GAME_SERVER_INSTANCE = "game_server_instance"