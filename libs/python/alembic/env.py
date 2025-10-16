"""Reusable env.py module for Alembic migrations.

This module provides a flexible env.py implementation that can be used by different projects.
It supports both online and offline migration modes, schema filtering, and custom metadata.

Usage in your migrations/env.py:
    ```python
    from alembic import context
    from libs.python.alembic.env import run_migrations
    from myapp.models import Base

    # Get your target metadata
    target_metadata = Base.metadata

    # Optional: Define schema filter
    def include_object(object, name, type_, reflected, compare_to):
        if type_ == "table":
            return object.schema == "myschema"
        return True

    # Run migrations with your configuration
    run_migrations(
        context=context,
        target_metadata=target_metadata,
        include_object=include_object,  # Optional
        include_schemas=True,  # Optional, default True
    )
    ```
"""

import logging
from typing import Any, Callable, Optional

from alembic import context as alembic_context
from sqlalchemy import MetaData

logger = logging.getLogger(__name__)


def run_migrations(
    context: Any = None,
    target_metadata: Optional[MetaData] = None,
    include_object: Optional[Callable] = None,
    include_schemas: bool = True,
    version_table_schema: Optional[str] = None,
) -> None:
    """Run Alembic migrations in online or offline mode.

    Args:
        context: Alembic context object. If None, uses alembic.context
        target_metadata: SQLAlchemy MetaData object containing models
        include_object: Optional callback to filter objects during autogenerate.
            Signature: include_object(object, name, type_, reflected, compare_to) -> bool
        include_schemas: Whether to include schema names in operations. Default: True
        version_table_schema: Schema for alembic_version table. If None, uses config value or "public"

    The function automatically detects whether to run in online or offline mode
    based on context.is_offline_mode().
    """
    if context is None:
        context = alembic_context

    config = context.config

    # Get version_table_schema from parameter, config, or default to "public"
    if version_table_schema is None:
        version_table_schema = config.get_main_option("version_table_schema", "public")

    if context.is_offline_mode():
        _run_migrations_offline(
            context=context,
            config=config,
            target_metadata=target_metadata,
            include_schemas=include_schemas,
        )
    else:
        _run_migrations_online(
            context=context,
            config=config,
            target_metadata=target_metadata,
            include_object=include_object,
            include_schemas=include_schemas,
            version_table_schema=version_table_schema,
        )


def _run_migrations_offline(
    context: Any,
    config: Any,
    target_metadata: Optional[MetaData],
    include_schemas: bool,
) -> None:
    """Run migrations in 'offline' mode.

    This configures the context with just a URL and not an Engine.
    By skipping the Engine creation we don't even need a DBAPI to be available.

    Calls to context.execute() here emit the given string to the script output.
    """
    logger.info("Running migrations in offline mode")

    url = config.get_main_option("sqlalchemy.url")
    context.configure(
        url=url,
        target_metadata=target_metadata,
        literal_binds=True,
        dialect_opts={"paramstyle": "named"},
        include_schemas=include_schemas,
    )

    with context.begin_transaction():
        context.run_migrations()

    logger.info("Offline migrations completed")


def _run_migrations_online(
    context: Any,
    config: Any,
    target_metadata: Optional[MetaData],
    include_object: Optional[Callable],
    include_schemas: bool,
    version_table_schema: str,
) -> None:
    """Run migrations in 'online' mode.

    In this scenario we need to create an Engine and associate a connection
    with the context.
    """
    logger.info("Running migrations in online mode")

    # Get the connection from config attributes
    # This is set by migration utilities (run_migration, create_migration, etc.)
    connectable = config.attributes.get("connection")

    if connectable is None:
        raise RuntimeError(
            "No connection available in config.attributes['connection']. "
            "Make sure to set it before calling run_migrations()."
        )

    # Build context.configure() kwargs
    configure_kwargs = {
        "connection": connectable,
        "target_metadata": target_metadata,
        "include_schemas": include_schemas,
        "version_table_schema": version_table_schema,
    }

    # Add include_object if provided
    if include_object is not None:
        configure_kwargs["include_object"] = include_object

    context.configure(**configure_kwargs)

    with context.begin_transaction():
        context.run_migrations()

    logger.info("Online migrations completed")
