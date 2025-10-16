"""Alembic configuration utilities.

Provides functions to create and configure Alembic Config objects programmatically
without requiring alembic.ini files. This is essential for containerized deployments.
"""

import logging
import os
from typing import Optional

import alembic.config

logger = logging.getLogger(__name__)


def create_alembic_config(
    migrations_dir: str,
    database_url: str,
    file_template: Optional[str] = None,
    version_table_schema: Optional[str] = "public",
) -> alembic.config.Config:
    """Create an Alembic configuration programmatically.

    Args:
        migrations_dir: Path to the migrations directory containing env.py
        database_url: SQLAlchemy database URL
        file_template: Optional template for migration filenames.
            Default: "%%(year)d_%%(month).2d_%%(day).2d_%%(hour).2d%%(minute).2d-%%(rev)s_%%(slug)s"
        version_table_schema: Schema to store the alembic_version table. Default: "public"

    Returns:
        Configured alembic.config.Config object

    Example:
        >>> import importlib.resources
        >>> migrations_dir = str(importlib.resources.files("myapp.migrations"))
        >>> config = create_alembic_config(
        ...     migrations_dir=migrations_dir,
        ...     database_url="postgresql://user:pass@localhost/db"
        ... )
    """
    # Validate migrations directory exists and has env.py
    env_py_path = os.path.join(migrations_dir, "env.py")
    if not os.path.exists(env_py_path):
        raise FileNotFoundError(
            f"env.py not found in migrations directory: {migrations_dir}"
        )

    # Configure Alembic programmatically without requiring alembic.ini
    # Pass file_=None to indicate we're configuring programmatically
    config = alembic.config.Config(file_=None, ini_section="alembic")

    # Set the script location - this is required by Alembic
    config.set_main_option("script_location", migrations_dir)

    # Set the database URL (used by migrations in offline mode)
    config.set_main_option("sqlalchemy.url", database_url)

    # Set the file template for migration filenames
    if file_template is None:
        file_template = (
            "%%(year)d_%%(month).2d_%%(day).2d_%%(hour).2d%%(minute).2d"
            "-%%(rev)s_%%(slug)s"
        )
    config.set_main_option("file_template", file_template)

    # Store version_table_schema for use in env.py
    if version_table_schema:
        config.set_main_option("version_table_schema", version_table_schema)

    logger.debug(f"Created alembic config for migrations in: {migrations_dir}")

    return config
