"""
RabbitMQ utilities for message publishing and subscription.

This package provides reusable components for working with RabbitMQ:
- Connection management with SSL support
- Publisher and Subscriber implementations
- Configuration dataclasses for routing, binding, and queues
- Utility functions for routing key manipulation
"""

from libs.python.rmq.config import (
    BindingConfig,
    ExchangeRegistry,
    MessageTypeRegistry,
    QueueConfig,
    RoutingKeyConfig,
    TopicWildcard,
)
from libs.python.rmq.connection import (
    cleanup_rabbitmq_connections,
    create_rabbitmq_vhost,
    get_rabbitmq_connection,
    get_rabbitmq_ssl_options,
    init_rabbitmq,
    init_rabbitmq_from_config,
)
from libs.python.rmq.interface import (
    MessagePublisherInterface,
    MessageSubscriberInterface,
)
from libs.python.rmq.publisher import RabbitPublisher
from libs.python.rmq.subscriber import RabbitSubscriber
from libs.python.rmq.util import add_routing_key_prefix, add_routing_key_suffix

__all__ = [
    # Config
    "BindingConfig",
    "ExchangeRegistry",
    "MessageTypeRegistry",
    "QueueConfig",
    "RoutingKeyConfig",
    "TopicWildcard",
    # Connection
    "cleanup_rabbitmq_connections",
    "create_rabbitmq_vhost",
    "get_rabbitmq_connection",
    "get_rabbitmq_ssl_options",
    "init_rabbitmq",
    "init_rabbitmq_from_config",
    # Interfaces
    "MessagePublisherInterface",
    "MessageSubscriberInterface",
    # Publisher/Subscriber
    "RabbitPublisher",
    "RabbitSubscriber",
    # Utilities
    "add_routing_key_prefix",
    "add_routing_key_suffix",
]
