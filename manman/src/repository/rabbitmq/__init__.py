"""
ManMan RabbitMQ module.

This module provides ManMan-specific RabbitMQ implementations built on top of
the generic libs.python.rmq library.
"""

# Import config classes and utilities (don't require amqpstorm)
from manman.src.repository.rabbitmq.config import (
    BindingConfig,
    ExchangeRegistry,
    MessageTypeRegistry,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
)
from manman.src.repository.rabbitmq.util import (
    add_routing_key_prefix,
    add_routing_key_suffix,
)

# Lazy imports for classes that require amqpstorm
def __getattr__(name):
    if name == "RabbitPublisher":
        from manman.src.repository.rabbitmq.publisher import RabbitPublisher
        return RabbitPublisher
    elif name == "RabbitSubscriber":
        from manman.src.repository.rabbitmq.subscriber import RabbitSubscriber
        return RabbitSubscriber
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")

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
