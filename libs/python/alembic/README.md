# Consolidated Alembic Library

This library provides reusable components for database migrations using Alembic. It consolidates common patterns from the monorepo's migration implementations.

## Features

- **Programmatic Configuration**: No alembic.ini required - works in containerized environments
- **Reusable env.py**: Flexible implementation supporting online/offline modes and schema filtering
- **Migration Utilities**: High-level functions for running, creating, and managing migrations
- **Unified CLI**: Typer-based CLI that can be integrated into existing applications
- **Bazel Compatible**: Works with Bazel packaging and importlib.resources

## Quick Start

### 1. Set up your migrations directory

Your migrations directory should have this structure:
```
myapp/
  migrations/
    __init__.py
    env.py          # Use the template below
    script.py.mako  # Standard Alembic template
    versions/       # Migration files go here
      __init__.py
```

### 2. Create migrations/env.py

Use the consolidated env.py module:

```python
"""Alembic environment configuration."""

from alembic import context
from libs.python.alembic.env import run_migrations

# Import your models
from myapp.models import Base

# Configure and run migrations
target_metadata = Base.metadata

# Optional: Define schema filter
def include_object(object, name, type_, reflected, compare_to):
    """Filter objects during autogenerate."""
    if type_ == "table":
        # Only include tables from your schema
        return object.schema == "myschema"
    return True

# Run migrations
run_migrations(
    context=context,
    target_metadata=target_metadata,
    include_object=include_object,  # Optional
    include_schemas=True,  # Optional, default True
)
```

### 3. Option A: Use the CLI in your application

```python
# myapp/cli.py
import typer
from libs.python.alembic.cli import create_migration_app
from myapp.models import Base

app = typer.Typer()

# Create migration CLI
migration_cli = create_migration_app(
    migrations_package="myapp.migrations",
    target_metadata=Base.metadata,
    database_url_envvar="DATABASE_URL",
    version_table_schema="public",
)

# Add to your main CLI
app.add_typer(migration_cli, name="migration")

if __name__ == "__main__":
    app()
```

Then use it:
```bash
export DATABASE_URL="postgresql://user:pass@localhost/db"
python -m myapp.cli migration run
python -m myapp.cli migration check
python -m myapp.cli migration create "add user table"
python -m myapp.cli migration downgrade -1
```

### 3. Option B: Use the utilities directly

```python
# myapp/migration_runner.py
import os
from sqlalchemy import create_engine

try:
    from importlib.resources import files
except ImportError:
    from importlib_resources import files

from libs.python.alembic import (
    create_alembic_config,
    run_migration,
    should_run_migration,
)

# Get migrations directory
migrations_dir = str(files("myapp.migrations"))

# Create engine and config
database_url = os.environ["DATABASE_URL"]
engine = create_engine(database_url, pool_pre_ping=True)
config = create_alembic_config(
    migrations_dir=migrations_dir,
    database_url=database_url,
)

# Run migrations
if should_run_migration(engine, config):
    run_migration(engine, config)
```

## API Reference

### Config Module (`libs.python.alembic.config`)

#### `create_alembic_config()`
Create an Alembic configuration programmatically.

**Parameters:**
- `migrations_dir` (str): Path to migrations directory containing env.py
- `database_url` (str): SQLAlchemy database URL
- `file_template` (Optional[str]): Template for migration filenames
- `version_table_schema` (Optional[str]): Schema for alembic_version table (default: "public")

**Returns:** `alembic.config.Config`

### Migration Module (`libs.python.alembic.migration`)

#### `run_migration(engine, config)`
Run database migrations to head.

#### `should_run_migration(engine, config)`
Check if there are pending migrations. Returns bool.

#### `create_migration(engine, config, message=None)`
Create a new migration based on model changes.

#### `run_downgrade(engine, config, revision)`
Downgrade database to a specific revision.

### Env Module (`libs.python.alembic.env`)

#### `run_migrations()`
Run Alembic migrations in online or offline mode. Called from your migrations/env.py.

**Parameters:**
- `context`: Alembic context object
- `target_metadata`: SQLAlchemy MetaData object
- `include_object`: Optional callback to filter objects
- `include_schemas`: Whether to include schema names (default: True)
- `version_table_schema`: Schema for alembic_version table

### CLI Module (`libs.python.alembic.cli`)

#### `create_migration_app()`
Create a Typer CLI application for database migrations.

**Parameters:**
- `migrations_package` (str): Python package path to migrations
- `target_metadata` (Optional[MetaData]): SQLAlchemy MetaData object
- `database_url_envvar` (str): Environment variable name for database URL (default: "DATABASE_URL")
- `include_object` (Optional[Callable]): Filter callback
- `version_table_schema` (str): Schema for alembic_version table (default: "public")

**Returns:** `typer.Typer` application

## Migration from Existing Implementations

### From manman

Replace direct usage in `manman/src/host/main.py`:

```python
# Old:
config = _get_alembic_config()
alembic.command.upgrade(config, "head")

# New:
from libs.python.alembic import create_alembic_config, run_migration
migrations_dir = str(files("manman.src.migrations"))
config = create_alembic_config(migrations_dir, database_url)
run_migration(engine, config)
```

Update `manman/src/migrations/env.py`:
```python
from alembic import context
from libs.python.alembic.env import run_migrations
from manman.src.models import ManManBase

def include_object(object, name, type_, reflected, compare_to):
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
```

### From friendly_computing_machine

Update `friendly_computing_machine/src/friendly_computing_machine/cli/migration_cli.py`:

```python
from libs.python.alembic.cli import create_migration_app
from friendly_computing_machine.src.friendly_computing_machine.models import Base

migration_app = create_migration_app(
    migrations_package="friendly_computing_machine.src.migrations",
    target_metadata=Base.metadata,
    database_url_envvar="DATABASE_URL",
)
```

Update `friendly_computing_machine/src/migrations/env.py`:
```python
from alembic import context
from libs.python.alembic.env import run_migrations
from friendly_computing_machine.src.friendly_computing_machine.models.slack import Base

run_migrations(
    context=context,
    target_metadata=Base.metadata,
    include_schemas=True,
    version_table_schema="public",
)
```

## Benefits

1. **DRY Principle**: Single source of truth for migration logic
2. **Consistency**: All projects use the same patterns
3. **Maintainability**: Fix bugs/add features in one place
4. **Testability**: Easier to write tests for migration logic
5. **Documentation**: Centralized documentation and examples
6. **Flexibility**: Can be used with or without the CLI component

## BUILD.bazel Integration

Add dependency to your BUILD.bazel:

```starlark
py_library(
    name = "migrations",
    srcs = glob(["**/*.py"]),
    data = glob(["**/*.mako", "**/README"]),
    deps = [
        "//libs/python/alembic",  # Add this
        "//myapp/models",
        "@pypi//:alembic",
    ],
)
```

## Testing

The library is designed to be testable. Example test:

```python
from unittest.mock import Mock, patch
from libs.python.alembic import create_alembic_config, run_migration

def test_run_migration():
    engine = Mock()
    config = create_alembic_config(
        migrations_dir="/path/to/migrations",
        database_url="postgresql://test",
    )
    
    with patch("alembic.command.upgrade") as mock_upgrade:
        run_migration(engine, config)
        mock_upgrade.assert_called_once()
```

## Troubleshooting

### "env.py not found in migrations directory"
Ensure your migrations directory has an `env.py` file using the template from this README.

### "No connection available in config.attributes['connection']"
When using the env module directly, ensure you set the connection:
```python
with engine.begin() as connection:
    config.attributes["connection"] = connection
    # Now call alembic commands
```

The migration utilities handle this automatically.

### ImportError with importlib.resources
For Python < 3.9, ensure `importlib_resources` is installed:
```
pip install importlib_resources
```

## Contributing

When adding features to this library:
1. Update all modules (config, migration, env, cli) as needed
2. Update this README with new examples
3. Add tests if possible
4. Update dependent projects (manman, friendly_computing_machine)
