"""
Tests for the postgres repository base class.

This module tests the DatabaseRepository class to ensure:
1. Sessions are properly closed to prevent connection leaks
2. Persistent sessions are not closed by the repository
3. Session factories work correctly
4. Context managers function as expected
"""

from unittest.mock import Mock, patch

import pytest

from libs.python.postgres import DatabaseRepository


class TestDatabaseRepository:
    """Tests for the DatabaseRepository class."""

    def test_init_with_session(self):
        """Test initialization with a persistent session."""
        mock_session = Mock()
        repo = DatabaseRepository(session=mock_session)
        
        assert repo._session == mock_session
        assert repo._session_factory is None

    def test_init_with_session_factory(self):
        """Test initialization with a session factory."""
        mock_factory = Mock()
        repo = DatabaseRepository(session_factory=mock_factory)
        
        assert repo._session is None
        assert repo._session_factory == mock_factory

    def test_create_session_with_factory(self):
        """Test that _create_session uses the session factory."""
        mock_session = Mock()
        mock_factory = Mock(return_value=mock_session)
        repo = DatabaseRepository(session_factory=mock_factory)
        
        session = repo._create_session()
        
        assert session == mock_session
        mock_factory.assert_called_once()

    def test_create_session_not_implemented(self):
        """Test that _create_session raises NotImplementedError without factory."""
        repo = DatabaseRepository()
        
        with pytest.raises(NotImplementedError):
            repo._create_session()

    def test_session_context_with_persistent_session(self):
        """Test that persistent sessions are not closed."""
        mock_session = Mock()
        repo = DatabaseRepository(session=mock_session)
        
        with repo._get_session_context() as session:
            assert session == mock_session
        
        # Session should NOT be closed
        mock_session.close.assert_not_called()

    def test_session_context_with_factory(self):
        """Test that created sessions are properly closed."""
        mock_session = Mock()
        mock_factory = Mock(return_value=mock_session)
        repo = DatabaseRepository(session_factory=mock_factory)
        
        with repo._get_session_context() as session:
            assert session == mock_session
        
        # Session SHOULD be closed
        mock_session.close.assert_called_once()

    def test_session_context_closes_on_exception(self):
        """Test that sessions are closed even when exceptions occur."""
        mock_session = Mock()
        mock_factory = Mock(return_value=mock_session)
        repo = DatabaseRepository(session_factory=mock_factory)
        
        with pytest.raises(ValueError):
            with repo._get_session_context() as session:
                assert session == mock_session
                raise ValueError("Test exception")
        
        # Session SHOULD still be closed
        mock_session.close.assert_called_once()

    def test_multiple_context_entries(self):
        """Test that multiple context entries each create and close sessions."""
        mock_session1 = Mock()
        mock_session2 = Mock()
        mock_factory = Mock(side_effect=[mock_session1, mock_session2])
        repo = DatabaseRepository(session_factory=mock_factory)
        
        # First context
        with repo._get_session_context() as session:
            assert session == mock_session1
        
        # Second context
        with repo._get_session_context() as session:
            assert session == mock_session2
        
        # Both sessions should be closed
        mock_session1.close.assert_called_once()
        mock_session2.close.assert_called_once()
        assert mock_factory.call_count == 2


class ConcreteRepository(DatabaseRepository):
    """Concrete repository for testing subclass pattern."""
    
    def __init__(self, engine):
        """Initialize with an engine."""
        from sqlmodel import Session
        super().__init__(session_factory=lambda: Session(engine))
    
    def get_items(self):
        """Example method that uses session context."""
        with self._get_session_context() as session:
            # In real use, would execute a query
            return session


class TestConcreteRepository:
    """Test a concrete repository implementation."""
    
    def test_concrete_repository_usage(self):
        """Test that concrete repositories work as expected."""
        mock_engine = Mock()
        mock_session = Mock()
        
        with patch('sqlmodel.Session', return_value=mock_session):
            repo = ConcreteRepository(mock_engine)
            result = repo.get_items()
            
            # Should get the session
            assert result == mock_session
            
            # Session should be closed after method returns
            mock_session.close.assert_called_once()
