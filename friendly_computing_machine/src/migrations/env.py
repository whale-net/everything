"""Alembic environment configuration for friendly_computing_machine using consolidated library."""

from alembic import context
from libs.python.alembic.env import run_migrations

# Import models to get metadata
from friendly_computing_machine.src.friendly_computing_machine.models.slack import Base

# Get target metadata from models
target_metadata = Base.metadata

# Run migrations using consolidated library
run_migrations(
    context=context,
    target_metadata=target_metadata,
    include_schemas=True,
    version_table_schema="public",
)
