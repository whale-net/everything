"""
ManMan-specific RabbitMQ configuration.

This module extends the generic RabbitMQ library with ManMan-specific
exchange and message type registries.
"""

from dataclasses import dataclass
from enum import StrEnum
from typing import Union

# Re-export generic RabbitMQ configuration classes
from libs.python.rmq import (
    BindingConfig as GenericBindingConfig,
    QueueConfig,
    RoutingKeyConfig as GenericRoutingKeyConfig,
    TopicWildcard,
)

from manman.src.constants import EntityRegistry


class ExchangeRegistry(StrEnum):
    """ManMan-specific exchange registry."""
    # NOTE: for now, all durable topic exchanges
    INTERNAL_SERVICE_EVENT = "internal_service_events"
    EXTERNAL_SERVICE_EVENT = "external_service_events"


class MessageTypeRegistry(StrEnum):
    """ManMan-specific message type registry."""
    STATUS = "status"
    COMMAND = "command"


# ManMan-specific RoutingKeyConfig that uses EntityRegistry and MessageTypeRegistry
@dataclass
class RoutingKeyConfig:
    """
    ManMan-specific routing key configuration.
    
    Extends the generic RoutingKeyConfig to work with ManMan's
    EntityRegistry and MessageTypeRegistry enums.
    """
    entity: Union[EntityRegistry, TopicWildcard]
    identifier: Union[str, TopicWildcard]
    type: Union[MessageTypeRegistry, TopicWildcard]
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


# ManMan-specific BindingConfig that uses ExchangeRegistry
@dataclass
class BindingConfig:
    """
    ManMan-specific binding configuration.
    
    Extends the generic BindingConfig to work with ManMan's ExchangeRegistry.
    """
    exchange: Union[ExchangeRegistry, str]
    routing_keys: list[Union[RoutingKeyConfig, str]]
