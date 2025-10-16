"""
RabbitMQ subscriber implementations for ManMan.

This module re-exports the generic RabbitMQ subscriber and provides
ManMan-specific subscriber interfaces.
"""

# Re-export the generic RabbitMQ subscriber
from libs.python.rmq import RabbitSubscriber

# Re-export for backward compatibility
from manman.src.repository.message.abstract_interface import MessageSubscriberInterface

__all__ = ["RabbitSubscriber", "MessageSubscriberInterface"]
