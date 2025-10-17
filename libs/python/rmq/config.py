"""
RabbitMQ configuration dataclasses.

This module provides configuration objects for RabbitMQ routing, binding, and queues.
"""

from dataclasses import dataclass, field
from enum import StrEnum
from typing import Optional, Union


class ExchangeRegistry(StrEnum):
    """Registry of RabbitMQ exchanges. Extend this in your application."""
    # NOTE: for now, all durable topic exchanges
    INTERNAL_SERVICE_EVENT = "internal_service_events"
    EXTERNAL_SERVICE_EVENT = "external_service_events"


class MessageTypeRegistry(StrEnum):
    """Registry of message types. Extend this in your application."""
    STATUS = "status"
    COMMAND = "command"


class TopicWildcard(StrEnum):
    """RabbitMQ topic wildcards for routing key patterns."""
    ALL = "#"  # Matches zero or more words
    ANY = "*"  # Matches exactly one word


@dataclass
class RoutingKeyConfig:
    """
    Configuration for RabbitMQ routing keys.
    
    Routing keys follow the pattern: entity.identifier.type[.subtype]
    """
    entity: Union[str, TopicWildcard]
    identifier: Union[str, TopicWildcard]
    type: Union[MessageTypeRegistry, TopicWildcard, str]
    subtype: Union[str, TopicWildcard, None] = None

    def build_key(self) -> str:
        """Build the routing key string from components."""
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
    """
    Configuration for RabbitMQ queues.
    
    Attributes:
        name: Queue name (use empty string for server-generated name)
        durable: Queue survives broker restart
        exclusive: Queue can only be used by one connection
        auto_delete: Queue is deleted when last consumer unsubscribes
        actual_queue_name: Set automatically after queue declaration
    """
    name: str
    durable: bool
    exclusive: bool
    auto_delete: bool
    actual_queue_name: Optional[str] = field(default=None, init=False)

    def build_name(self) -> str:
        """Build the queue name."""
        return self.name


@dataclass
class BindingConfig:
    """
    Configuration for binding queues to exchanges with routing keys.
    
    Attributes:
        exchange: Exchange name from ExchangeRegistry
        routing_keys: List of routing key configurations
    """
    exchange: Union[ExchangeRegistry, str]
    routing_keys: list[RoutingKeyConfig]
