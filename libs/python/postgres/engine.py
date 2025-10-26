"""
SQLAlchemy engine configuration with production-ready defaults.

This module provides engine creation utilities with sensible pool settings
for production use, preventing connection exhaustion under load.
"""

import os
from typing import Optional

import sqlalchemy
from sqlalchemy.engine import Engine


def create_engine(
    connection_string: str,
    pool_size: Optional[int] = None,
    max_overflow: Optional[int] = None,
    pool_recycle: Optional[int] = None,
    pool_pre_ping: bool = True,
    echo: bool = False,
    **kwargs,
) -> Engine:
    """
    Create a SQLAlchemy engine with production-ready pool settings.
    
    Default pool settings are configured for high-concurrency environments:
    - pool_size: 20 (default 5) - Number of persistent connections
    - max_overflow: 30 (default 10) - Additional connections when pool exhausted
    - pool_recycle: 3600 (default -1) - Recycle connections after 1 hour
    - pool_pre_ping: True - Verify connections before using them
    
    These defaults support up to 50 concurrent database operations, suitable for
    production FastAPI applications with multiple gunicorn workers.
    
    Args:
        connection_string: Database connection string (e.g., postgresql://...)
        pool_size: Number of persistent connections in pool (default: 20)
        max_overflow: Max temporary connections beyond pool_size (default: 30)
        pool_recycle: Recycle connections after N seconds (default: 3600)
        pool_pre_ping: Test connections before using (default: True)
        echo: Log all SQL statements (default: False)
        **kwargs: Additional engine arguments
        
    Returns:
        Configured SQLAlchemy Engine
        
    Environment Variables:
        SQLALCHEMY_POOL_SIZE: Override default pool_size
        SQLALCHEMY_MAX_OVERFLOW: Override default max_overflow
        SQLALCHEMY_POOL_RECYCLE: Override default pool_recycle
        
    Example:
        >>> from libs.python.postgres.engine import create_engine
        >>> engine = create_engine("postgresql://user:pass@localhost/db")
        >>> # Engine now supports 50 concurrent connections (20 + 30 overflow)
        
        >>> # Custom pool settings
        >>> engine = create_engine(
        ...     "postgresql://user:pass@localhost/db",
        ...     pool_size=10,
        ...     max_overflow=20,
        ... )
    """
    # Apply environment variable overrides or defaults
    if pool_size is None:
        pool_size = int(os.environ.get("SQLALCHEMY_POOL_SIZE", "20"))
    
    if max_overflow is None:
        max_overflow = int(os.environ.get("SQLALCHEMY_MAX_OVERFLOW", "30"))
    
    if pool_recycle is None:
        pool_recycle = int(os.environ.get("SQLALCHEMY_POOL_RECYCLE", "3600"))
    
    return sqlalchemy.create_engine(
        connection_string,
        pool_size=pool_size,
        max_overflow=max_overflow,
        pool_recycle=pool_recycle,
        pool_pre_ping=pool_pre_ping,
        echo=echo,
        **kwargs,
    )
