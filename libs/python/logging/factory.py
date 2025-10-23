"""Logger factory and convenience functions.

Provides easy access to loggers with automatic context injection.
"""

import logging
from typing import Optional

from libs.python.logging.context import get_context, update_context


class ContextLogger(logging.LoggerAdapter):
    """Logger adapter that automatically includes context in all log records.
    
    This extends the standard LoggerAdapter to automatically inject context
    attributes into log records.
    """
    
    def process(self, msg, kwargs):
        """Process log call to inject context.
        
        Args:
            msg: Log message
            kwargs: Log call keyword arguments
            
        Returns:
            Tuple of (msg, kwargs) with context injected
        """
        # Get current context
        context = get_context()
        
        # Inject context into extra
        extra = kwargs.get("extra", {})
        
        if context:
            # Add all context fields to extra
            context_dict = context.to_dict()
            # Don't override explicit extra values
            for key, value in context_dict.items():
                if key not in extra:
                    extra[key] = value
        
        kwargs["extra"] = extra
        
        return msg, kwargs


def get_logger(name: str) -> ContextLogger:
    """Get a logger with automatic context injection.
    
    This is the primary way to get a logger in the application. The returned
    logger will automatically include all context attributes in log records.
    
    Args:
        name: Logger name (typically __name__)
        
    Returns:
        ContextLogger that includes context in all records
        
    Example:
        >>> logger = get_logger(__name__)
        >>> logger.info("Processing request")  # Automatically includes context
    """
    base_logger = logging.getLogger(name)
    return ContextLogger(base_logger, {})


def log_with_context(
    logger: logging.Logger,
    level: int,
    message: str,
    **context_updates,
) -> None:
    """Log a message with temporary context updates.
    
    This is useful for one-off log calls where you want to add context
    without permanently updating the global context.
    
    Args:
        logger: Logger to use
        level: Log level (logging.INFO, logging.ERROR, etc.)
        message: Log message
        **context_updates: Temporary context attributes
        
    Example:
        >>> log_with_context(
        ...     logger, logging.INFO, "Processing",
        ...     request_id="abc-123",
        ...     user_id="user-456",
        ... )
    """
    # Use extra to avoid polluting global context
    logger.log(level, message, extra=context_updates)


# Convenience functions that match logging module API
def debug(message: str, **kwargs) -> None:
    """Log debug message with context.
    
    Args:
        message: Debug message
        **kwargs: Additional context or standard logging kwargs
    """
    logger = get_logger("libs.python.logging")
    logger.debug(message, **kwargs)


def info(message: str, **kwargs) -> None:
    """Log info message with context.
    
    Args:
        message: Info message
        **kwargs: Additional context or standard logging kwargs
    """
    logger = get_logger("libs.python.logging")
    logger.info(message, **kwargs)


def warning(message: str, **kwargs) -> None:
    """Log warning message with context.
    
    Args:
        message: Warning message
        **kwargs: Additional context or standard logging kwargs
    """
    logger = get_logger("libs.python.logging")
    logger.warning(message, **kwargs)


def error(message: str, **kwargs) -> None:
    """Log error message with context.
    
    Args:
        message: Error message
        **kwargs: Additional context or standard logging kwargs
    """
    logger = get_logger("libs.python.logging")
    logger.error(message, **kwargs)


def critical(message: str, **kwargs) -> None:
    """Log critical message with context.
    
    Args:
        message: Critical message
        **kwargs: Additional context or standard logging kwargs
    """
    logger = get_logger("libs.python.logging")
    logger.critical(message, **kwargs)


def exception(message: str, **kwargs) -> None:
    """Log exception with traceback and context.
    
    Args:
        message: Exception message
        **kwargs: Additional context or standard logging kwargs
    """
    logger = get_logger("libs.python.logging")
    logger.exception(message, **kwargs)
