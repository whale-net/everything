"""Tests for engine configuration utilities."""

import os
import pytest
from unittest.mock import patch

from libs.python.postgres.engine import create_engine


def test_create_engine_with_defaults():
    """Test engine creation with default pool settings."""
    engine = create_engine("sqlite:///:memory:")
    
    # Verify default pool settings
    assert engine.pool.size() == 20
    assert engine.pool._max_overflow == 30
    assert engine.pool._recycle == 3600
    assert engine.pool._timeout == 30  # Default pool timeout
    assert engine.pool._pre_ping is True


def test_create_engine_with_custom_settings():
    """Test engine creation with custom pool settings."""
    engine = create_engine(
        "sqlite:///:memory:",
        pool_size=10,
        max_overflow=15,
        pool_recycle=1800,
        pool_timeout=15,
    )
    
    assert engine.pool.size() == 10
    assert engine.pool._max_overflow == 15
    assert engine.pool._recycle == 1800
    assert engine.pool._timeout == 15


def test_create_engine_with_env_overrides():
    """Test engine creation respects environment variables."""
    with patch.dict(os.environ, {
        "SQLALCHEMY_POOL_SIZE": "15",
        "SQLALCHEMY_MAX_OVERFLOW": "25",
        "SQLALCHEMY_POOL_RECYCLE": "7200",
    }):
        engine = create_engine("sqlite:///:memory:")
        
        assert engine.pool.size() == 15
        assert engine.pool._max_overflow == 25
        assert engine.pool._recycle == 7200


def test_create_engine_explicit_overrides_env():
    """Test explicit arguments override environment variables."""
    with patch.dict(os.environ, {
        "SQLALCHEMY_POOL_SIZE": "15",
        "SQLALCHEMY_MAX_OVERFLOW": "25",
    }):
        engine = create_engine(
            "sqlite:///:memory:",
            pool_size=30,
            max_overflow=40,
        )
        
        # Explicit args should win
        assert engine.pool.size() == 30
        assert engine.pool._max_overflow == 40


def test_create_engine_pre_ping_disabled():
    """Test pool_pre_ping can be disabled."""
    engine = create_engine(
        "sqlite:///:memory:",
        pool_pre_ping=False,
    )
    
    assert engine.pool._pre_ping is False
