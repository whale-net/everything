"""
RabbitMQ utility functions for ManMan.

This module re-exports the generic RabbitMQ utility functions.
"""

# Re-export utility functions from the generic library
from libs.python.rmq.util import add_routing_key_prefix, add_routing_key_suffix

__all__ = ["add_routing_key_prefix", "add_routing_key_suffix"]
