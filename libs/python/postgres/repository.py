"""
Reusable database repository base class with proper session management.

This module provides a base repository class that:
1. Prevents database connection leaks
2. Supports both persistent and per-operation sessions
3. Works with SQLModel/SQLAlchemy
4. Can be extended by any project in the monorepo

Example usage:
    ```python
    from libs.python.postgres import DatabaseRepository
    
    class MyRepository(DatabaseRepository):
        def get_users(self):
            with self._get_session_context() as session:
                return session.exec(select(User)).all()
    
    # Use with automatic session management
    repo = MyRepository()
    users = repo.get_users()  # Session is automatically closed
    
    # Or provide a persistent session
    with Session(engine) as session:
        repo = MyRepository(session=session)
        users = repo.get_users()  # Uses provided session
    ```
"""

from contextlib import contextmanager
from typing import Callable, Optional

from sqlmodel import Session


class DatabaseRepository:
    """
    Base repository class with proper database session management.
    
    This class provides a foundation for database operations that prevents
    connection leaks by ensuring sessions are always properly closed.
    
    Attributes:
        _session: Optional persistent session. If provided, this session
                  will be used for all operations and will NOT be closed
                  by the repository.
        _session_factory: Optional callable that creates new sessions.
                          If not provided, subclasses must override
                          _create_session() method.
    """

    def __init__(
        self,
        session: Optional[Session] = None,
        session_factory: Optional[Callable[[], Session]] = None,
    ):
        """
        Initialize the repository with optional session or session factory.

        Args:
            session: Optional persistent SQLModel session. If provided, this
                    session will be used for all operations and will NOT be
                    closed by the repository.
            session_factory: Optional callable that creates new sessions.
                           Called when no persistent session is provided.
                           If not provided, subclasses must override
                           _create_session() method.
        """
        self._session = session
        self._session_factory = session_factory

    def _create_session(self) -> Session:
        """
        Create a new database session.
        
        Subclasses should override this method if they don't provide
        a session_factory in __init__.
        
        Returns:
            A new SQLModel Session instance
            
        Raises:
            NotImplementedError: If neither session_factory nor this method
                               is implemented by subclass
        """
        if self._session_factory is not None:
            return self._session_factory()
        
        raise NotImplementedError(
            "Either provide a session_factory or override _create_session() method"
        )

    def _get_session_context(self):
        """
        Get a session context manager that properly handles session lifecycle.
        
        If a persistent session was provided in __init__, it will be yielded
        without being closed. Otherwise, a new session is created and will be
        automatically closed when the context exits.
        
        Returns:
            A context manager that yields a database session
            
        Example:
            ```python
            with self._get_session_context() as session:
                # Use session for database operations
                results = session.exec(select(Model)).all()
            # Session is automatically closed if it was created here
            ```
        """
        if self._session is not None:
            # Use persistent session without closing it
            @contextmanager
            def session_context():
                yield self._session

            return session_context()

        # Create a new session and ensure it's closed
        @contextmanager
        def session_context():
            session = self._create_session()
            try:
                yield session
            finally:
                session.close()

        return session_context()
