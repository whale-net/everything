"""Generic RabbitMQ configuration classes."""

from dataclasses import dataclass, field
from enum import StrEnum
from typing import Optional, Union


class TopicWildcard(StrEnum):
    """RabbitMQ topic wildcards for routing keys."""
    ALL = "#"
    ANY = "*"


@dataclass
class RoutingKeyConfig:
    """
    Configuration for RabbitMQ routing keys.
    
    Builds routing keys in the format: entity.identifier.type[.subtype]
    Supports wildcards for flexible message routing.
    """
    entity: Union[str, TopicWildcard]
    identifier: Union[str, TopicWildcard]
    type: Union[str, TopicWildcard]
    subtype: Union[str, TopicWildcard, None] = None

    def build_key(self) -> str:
        """Build the routing key string."""
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


@dataclass
class QueueConfig:
    """Configuration for RabbitMQ queues."""
    name: str
    durable: bool
    exclusive: bool
    auto_delete: bool

    actual_queue_name: Optional[str] = field(default=None, init=False)

    def build_name(self):
        """Build the queue name."""
        return self.name


@dataclass
class BindingConfig:
    """
    Configuration for binding queues to exchanges with routing keys.
    
    :param exchange: Exchange name to bind to
    :param routing_keys: List of routing key configurations for message routing
    """
    exchange: str
    routing_keys: list[Union[RoutingKeyConfig, str]]
