"""
Client wrappers for ManMan generated OpenAPI clients.

This module provides high-level wrappers around the auto-generated OpenAPI clients,
handling model translation between domain models and generated API models.
"""

from manman.clients.worker_dal_client import WorkerDALClient

__all__ = ["WorkerDALClient"]
