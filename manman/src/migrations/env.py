"""Alembic environment configuration for manman using consolidated library."""

from alembic import context
from libs.python.alembic.env import run_migrations

# Import models to get metadata
from manman.src.models import ManManBase

# Get target metadata from models
target_metadata = ManManBase.metadata


def include_object(object, name, type_, reflected, compare_to):
    """Filter objects to only include manman schema tables."""
    if type_ == "table":
        return object.schema == "manman"
    return True


# Run migrations using consolidated library
run_migrations(
    context=context,
    target_metadata=target_metadata,
    include_object=include_object,
    include_schemas=True,
    version_table_schema="public",
)
