"""
Generic RabbitMQ consumer/producer library.

This module provides reusable RabbitMQ patterns for publishing and subscribing to messages.
"""

# Import config classes (don't require amqpstorm)
from libs.python.rmq.config import (
    BindingConfig,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
)

# Import utility functions (don't require amqpstorm)
from libs.python.rmq.util import add_routing_key_prefix, add_routing_key_suffix

# Lazy imports for classes that require amqpstorm
# These will only be imported when actually used
def __getattr__(name):
    if name == "RabbitPublisher":
        from libs.python.rmq.publisher import RabbitPublisher
        return RabbitPublisher
    elif name == "RabbitSubscriber":
        from libs.python.rmq.subscriber import RabbitSubscriber
        return RabbitSubscriber
    raise AttributeError(f"module {__name__!r} has no attribute {name!r}")

__all__ = [
    "BindingConfig",
    "QueueConfig",
    "RoutingKeyConfig",
    "TopicWildcard",
    "RabbitPublisher",
    "RabbitSubscriber",
    "add_routing_key_prefix",
    "add_routing_key_suffix",
]
