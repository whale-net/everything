from dataclasses import dataclass, field
from enum import StrEnum
from typing import Optional, Union

from manman.src.constants import EntityRegistry


class ExchangeRegistry(StrEnum):
    # NOTE: for now, all durable topic exchanges
    INTERNAL_SERVICE_EVENT = "internal_service_events"
    EXTERNAL_SERVICE_EVENT = "external_service_events"


class MessageTypeRegistry(StrEnum):
    STATUS = "status"
    COMMAND = "command"


class TopicWildcard(StrEnum):
    ALL = "#"
    ANY = "*"


@dataclass
class RoutingKeyConfig:
    entity: Union[EntityRegistry, TopicWildcard]
    identifier: Union[str, TopicWildcard]
    type: Union[MessageTypeRegistry, TopicWildcard]
    subtype: Union[str, TopicWildcard, None] = None

    def build_key(self) -> str:
        entity_str = str(self.entity)
        identifier_str = str(self.identifier)
        type_str = str(self.type)

        if self.subtype is None:
            subtype_str = ""
        else:
            subtype_str = f".{self.subtype}"

        return f"{entity_str}.{identifier_str}.{type_str}{subtype_str}"

    def __str__(self) -> str:
        return self.build_key()

    # @classmethod
    # def from_string(cls, key: str) -> "RoutingKeyConfig":


@dataclass
class QueueConfig:
    # TODO - more customization options for queue name
    name: str

    durable: bool
    exclusive: bool
    auto_delete: bool

    actual_queue_name: Optional[str] = field(default=None, init=False)

    def build_name(self):
        return self.name


@dataclass
class BindingConfig:
    exchange: ExchangeRegistry
    routing_keys: list[RoutingKeyConfig]
