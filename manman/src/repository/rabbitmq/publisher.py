"""
RabbitMQ publisher implementations for ManMan.

This module re-exports the generic RabbitMQ publisher and provides
ManMan-specific publisher interfaces.
"""

# Re-export the generic RabbitMQ publisher
from libs.python.rmq import RabbitPublisher

# Re-export for backward compatibility
from manman.src.repository.message.abstract_interface import MessagePublisherInterface

__all__ = ["RabbitPublisher", "MessagePublisherInterface"]
