"""
ManMan RabbitMQ module.

This module provides ManMan-specific RabbitMQ implementations built on top of
the generic libs.python.rmq library.
"""

from manman.src.repository.rabbitmq.config import (
    BindingConfig,
    ExchangeRegistry,
    MessageTypeRegistry,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
)
from manman.src.repository.rabbitmq.publisher import RabbitPublisher
from manman.src.repository.rabbitmq.subscriber import RabbitSubscriber
from manman.src.repository.rabbitmq.util import (
    add_routing_key_prefix,
    add_routing_key_suffix,
)

__all__ = [
    "BindingConfig",
    "ExchangeRegistry",
    "MessageTypeRegistry",
    "QueueConfig",
    "RoutingKeyConfig",
    "TopicWildcard",
    "RabbitPublisher",
    "RabbitSubscriber",
    "add_routing_key_prefix",
    "add_routing_key_suffix",
]
