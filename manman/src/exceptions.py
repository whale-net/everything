"""
Custom exceptions for the ManMan application.

This module contains all custom exception classes used throughout the application.
"""

import datetime


class WorkerAlreadyClosedException(Exception):
    """Raised when attempting to perform an operation on a worker that has already been closed (has an end_date)."""

    def __init__(
        self, worker_id: int, end_date: datetime.datetime, message: str = None
    ):
        self.worker_id = worker_id
        self.end_date = end_date
        if message is None:
            message = f"Worker {worker_id} was already closed on {end_date.isoformat()}"
        super().__init__(message)


class GameServerInstanceAlreadyClosedException(Exception):
    """Raised when attempting to perform an operation on a game server instance that has already been closed (has an end_date)."""

    def __init__(
        self, instance_id: int, end_date: datetime.datetime, message: str = None
    ):
        self.instance_id = instance_id
        self.end_date = end_date
        if message is None:
            message = f"Game server instance {instance_id} was already closed on {end_date.isoformat()}"
        super().__init__(message)
