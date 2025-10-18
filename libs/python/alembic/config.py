"""Alembic configuration utilities.

Provides functions to create and configure Alembic Config objects programmatically
without requiring alembic.ini files. This is essential for containerized deployments.
"""

import logging
import os
import shutil
from pathlib import Path
from typing import Optional

import alembic.config

logger = logging.getLogger(__name__)


def get_default_script_template() -> str:
    """Get the path to the default script.py.mako template.
    
    Returns:
        Absolute path to the bundled script.py.mako template
    """
    # Get the directory containing this config.py file
    alembic_lib_dir = Path(__file__).parent
    template_path = alembic_lib_dir / "script.py.mako"
    
    if not template_path.exists():
        raise FileNotFoundError(
            f"Default script template not found: {template_path}"
        )
    
    return str(template_path)


def create_alembic_config(
    migrations_dir: str,
    database_url: str,
    file_template: Optional[str] = None,
    version_table_schema: Optional[str] = "public",
    script_template: Optional[str] = None,
) -> alembic.config.Config:
    """Create an Alembic configuration programmatically.

    Args:
        migrations_dir: Path to the migrations directory containing env.py
        database_url: SQLAlchemy database URL
        file_template: Optional template for migration filenames.
            Default: "%%(year)d_%%(month).2d_%%(day).2d_%%(hour).2d%%(minute).2d-%%(rev)s_%%(slug)s"
        version_table_schema: Schema to store the alembic_version table. Default: "public"
        script_template: Optional path to script.py.mako template.
            If None, uses the library's default template.

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

    # Copy the library's default script template to the migrations directory
    # Alembic expects script.py.mako to be in the migrations directory
    if script_template is None:
        script_template = get_default_script_template()
        
    # Copy the template to migrations directory if using the default and it's outdated
    migrations_template = os.path.join(migrations_dir, "script.py.mako")
    if script_template == get_default_script_template():
        # Only copy if template doesn't exist or is outdated
        if (
            not os.path.exists(migrations_template)
            or (
                os.path.exists(script_template)
                and os.path.exists(migrations_template)
                and os.path.getmtime(script_template) > os.path.getmtime(migrations_template)
            )
        ):
            shutil.copy2(script_template, migrations_template)
            logger.debug(f"Copied default script template to: {migrations_template}")

    # Store version_table_schema for use in env.py
    if version_table_schema:
        config.set_main_option("version_table_schema", version_table_schema)

    logger.debug(f"Created alembic config for migrations in: {migrations_dir}")
    logger.debug(f"Using script template: {script_template}")

    return config
