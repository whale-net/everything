"""
Generic RabbitMQ consumer/producer library.

This module provides reusable RabbitMQ patterns for publishing and subscribing to messages.
"""

from libs.python.rmq.config import (
    BindingConfig,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
)
from libs.python.rmq.publisher import RabbitPublisher
from libs.python.rmq.subscriber import RabbitSubscriber
from libs.python.rmq.util import add_routing_key_prefix, add_routing_key_suffix

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
