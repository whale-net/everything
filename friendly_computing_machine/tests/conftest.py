from unittest.mock import Mock

import alembic.config
import pytest
from sqlalchemy import Engine
from sqlmodel import Session


@pytest.fixture
def mock_engine() -> Mock:
    """Provides a mock SQLAlchemy Engine."""
    return Mock(spec=Engine)


@pytest.fixture
def mock_session() -> Mock:
    """Provides a mock SQLModel Session."""
    return Mock(spec=Session)


@pytest.fixture
def mock_alembic_config() -> Mock:
    """Provides a mock Alembic Config."""
    config = Mock(spec=alembic.config.Config)
    config.attributes = {}  # Common setup for alembic tests
    return config
