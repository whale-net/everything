import concurrent.futures
import datetime
import functools
import logging
import threading
import warnings
from typing import Optional

logger = logging.getLogger(__name__)


def ts_to_datetime(ts: str):
    # yes ts is a str in the event payload
    return datetime.datetime.fromtimestamp(float(ts))


def datetime_to_ts(dt: datetime.datetime) -> str:
    """
    Convert a datetime object to a string timestamp.

    Args:
        dt: datetime object to convert.

    Returns:
        String representation of the timestamp.
    """
    return str(dt.timestamp())


class NamedThreadPool(concurrent.futures.ThreadPoolExecutor):
    def submit(
        self, fn, /, thread_name: Optional[str] = None, *args, **kwargs
    ) -> concurrent.futures.Future:
        def exec_and_rename_thread():
            if thread_name is not None and len(thread_name) > 0:
                logger.debug(
                    f"renaming {threading.current_thread().name} to {thread_name}"
                )
                threading.current_thread().name = thread_name
            fn(*args, **kwargs)

        return super().submit(exec_and_rename_thread)


def deprecated(func):
    @functools.wraps(func)
    def wrapper(*args, **kwargs):
        warnings.warn(
            f"{func.__name__} is deprecated and will be removed in a future release.",
            DeprecationWarning,
            stacklevel=2,
        )
        return func(*args, **kwargs)

    return wrapper
