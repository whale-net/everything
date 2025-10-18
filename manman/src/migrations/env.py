"""Alembic environment configuration for manman."""

from alembic import context
from libs.python.alembic.env import run_migrations
from manman.src.models import ManManBase


def include_object(object, name, type_, reflected, compare_to):
    """Include only manman schema tables."""
    if type_ == "table":
        return object.schema == "manman"
    return True


run_migrations(
    context=context,
    target_metadata=ManManBase.metadata,
    include_object=include_object,
    include_schemas=True,
    version_table_schema="public",
)
