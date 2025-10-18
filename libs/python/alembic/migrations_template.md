# Migration Directory Template

This directory contains database migrations managed by Alembic using the consolidated `//libs/python/alembic` library.

## Structure

```
migrations/
├── __init__.py           # Makes this a Python package
├── README.md            # This file
├── env.py               # Alembic environment configuration (see template below)
├── script.py.mako       # Template for new migration files
└── versions/            # Directory containing migration files
    └── __init__.py
```

## Setup

### 1. Create `env.py`

Copy this template to your `env.py`:

```python
"""Alembic environment configuration using consolidated library."""

from alembic import context
from libs.python.alembic.env import run_migrations

# Import your models - adjust import path as needed
from YOUR_PACKAGE.models import Base  # TODO: Update this import

# Get target metadata from your models
target_metadata = Base.metadata

# Optional: Filter objects during autogenerate
# Uncomment and customize if you need schema filtering
# def include_object(object, name, type_, reflected, compare_to):
#     """Filter objects during autogenerate."""
#     if type_ == "table":
#         # Only include tables from specific schema
#         return object.schema == "YOUR_SCHEMA"
#     return True

# Run migrations using consolidated library
run_migrations(
    context=context,
    target_metadata=target_metadata,
    # include_object=include_object,  # Uncomment if using schema filtering
    include_schemas=True,
    version_table_schema="public",
)
```

### 2. Create `script.py.mako`

Use this standard template:

```mako
"""${message}

Revision ID: ${up_revision}
Revises: ${down_revision | comma,n}
Create Date: ${create_date}

"""
from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa
import sqlmodel
${imports if imports else ""}

# revision identifiers, used by Alembic.
revision: str = ${repr(up_revision)}
down_revision: Union[str, None] = ${repr(down_revision)}
branch_labels: Union[str, Sequence[str], None] = ${repr(branch_labels)}
depends_on: Union[str, Sequence[str], None] = ${repr(depends_on)}


def upgrade() -> None:
    ${upgrades if upgrades else "pass"}


def downgrade() -> None:
    ${downgrades if downgrades else "pass"}
```

### 3. Create `versions/__init__.py`

Empty file to make versions a package:
```python
# Migration versions
```

## Usage

### With CLI Integration

Integrate into your application's CLI:

```python
# your_app/cli.py
import typer
from libs.python.alembic.cli import create_migration_app
from your_package.models import Base

app = typer.Typer()

# Create migration CLI
migration_cli = create_migration_app(
    migrations_package="your_package.migrations",  # Adjust package path
    target_metadata=Base.metadata,
    database_url_envvar="DATABASE_URL",
)

app.add_typer(migration_cli, name="migration")

if __name__ == "__main__":
    app()
```

Then use:
```bash
export DATABASE_URL="postgresql://user:pass@localhost/db"
python -m your_app.cli migration run      # Run migrations
python -m your_app.cli migration check    # Check if migrations needed
python -m your_app.cli migration create "message"  # Create migration
python -m your_app.cli migration downgrade -1     # Downgrade one revision
```

### Programmatic Usage

```python
import os
from sqlalchemy import create_engine

try:
    from importlib.resources import files
except ImportError:
    from importlib_resources import files

from libs.python.alembic import create_alembic_config, run_migration

# Get migrations directory
migrations_dir = str(files("your_package.migrations"))

# Setup
database_url = os.environ["DATABASE_URL"]
engine = create_engine(database_url, pool_pre_ping=True)
config = create_alembic_config(
    migrations_dir=migrations_dir,
    database_url=database_url,
)

# Run migrations
run_migration(engine, config)
```

## BUILD.bazel

Update your BUILD.bazel to depend on the consolidated library:

```starlark
load("@rules_python//python:defs.bzl", "py_library")

py_library(
    name = "migrations",
    srcs = glob(["**/*.py"]),
    data = glob(["**/*.mako", "**/README*"]),
    visibility = ["//your_package:__subpackages__"],
    deps = [
        "//libs/python/alembic",  # Add this dependency
        "//your_package/models",
        "@pypi//:alembic",
    ],
)
```

## Migration as a Job/Service

For Kubernetes deployments, create a job binary:

```starlark
# In your top-level BUILD.bazel
release_app(
    name = "migration",
    binary_name = "//your_package/cli:migration_cli",
    language = "python",
    domain = "your-domain",
    description = "Database migration job",
    app_type = "job",
    args = ["migration", "run"],
)
```

This will create a Kubernetes Job that runs migrations on deployment.

## Best Practices

1. **Always test migrations**: Create test migrations first, verify them, then apply to production
2. **Use meaningful messages**: `python cli.py migration create "add user_preferences table"`
3. **Review generated migrations**: Alembic's autogenerate is smart but not perfect
4. **Version control**: Commit migration files to git
5. **Run in sequence**: Migrations should be run in order, handled automatically by Alembic
6. **Backup before downgrade**: Always backup before running downgrade operations

## Troubleshooting

### Cannot find migrations directory
Ensure your migrations package is properly installed and accessible. Check:
- Package has `__init__.py` 
- Package is listed in BUILD.bazel deps
- Package path matches in CLI configuration

### Schema filtering not working
Make sure your `include_object` function is defined in `env.py` and passed to `run_migrations()`.

### Connection errors
Verify DATABASE_URL is set and accessible:
```bash
echo $DATABASE_URL
psql $DATABASE_URL -c "SELECT 1"
```

## See Also

- [//libs/python/alembic/README.md](../alembic/README.md) - Main library documentation
- [Alembic Documentation](https://alembic.sqlalchemy.org/) - Official Alembic docs
