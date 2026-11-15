# Alembic Consolidated Library

This document describes the consolidated Alembic migration library at `//libs/python/alembic`.

## Why

Multiple projects in the monorepo need database migrations with Alembic. Without a shared library:
- Each project duplicates 100+ lines of boilerplate code
- Inconsistent patterns across projects
- Bug fixes must be applied in multiple places
- No standard approach for new projects

## What

The consolidated library provides everything needed for database migrations:

**Core Modules**:
- `config.py` - Programmatic Alembic configuration (no alembic.ini files needed)
- `migration.py` - High-level utilities (run, create, check, downgrade)
- `env.py` - Reusable environment module for migrations/env.py
- `cli.py` - Typer-based CLI framework

**Benefits**:
- Single source of truth for migration logic
- Works in containerized environments (no config files needed)
- Supports schema filtering for multi-tenant databases
- Bazel-compatible with importlib.resources
- Reduces per-project code by ~80-90%

## How

### For New Projects

#### 1. Create Migrations Directory

```
myapp/
  migrations/
    __init__.py
    env.py          # See template below
    script.py.mako  # Standard Alembic template
    versions/
      __init__.py
```

#### 2. Create `migrations/env.py`

```python
"""Alembic environment for myapp."""

from alembic import context
from libs.python.alembic.env import run_migrations
from myapp.models import Base

# Optional: Filter by schema
def include_object(object, name, type_, reflected, compare_to):
    if type_ == "table":
        return object.schema == "myschema"
    return True

run_migrations(
    context=context,
    target_metadata=Base.metadata,
    include_object=include_object,  # Optional
    include_schemas=True,
)
```

#### 3. Option A: Use CLI Integration

```python
# myapp/cli.py
import typer
from libs.python.alembic.cli import create_migration_app
from myapp.models import Base

app = typer.Typer()

    migration_app = create_migration_app(
        migrations_package="myapp.migrations",
        target_metadata=Base.metadata,
    )

app.add_typer(migration_cli, name="migration")
```

Commands available:
```bash
python -m myapp.cli migration run      # Run migrations
python -m myapp.cli migration check    # Check if migrations needed
python -m myapp.cli migration create "message"  # Create migration
python -m myapp.cli migration downgrade -1     # Downgrade
```

#### 4. Option B: Use Utilities Directly

```python
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

# Setup
migrations_dir = str(files("myapp.migrations"))
```python
database_url = os.environ["POSTGRES_URL"]
engine = create_engine(database_url, pool_pre_ping=True)
config = create_alembic_config(
    migrations_dir="myapp/migrations",
    database_url=database_url,
)

# Run migrations
if should_run_migration(engine, config):
    run_migration(engine, config)
```

#### 5. Add Bazel Dependency

```starlark
# In your migrations BUILD.bazel
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

### For Existing Projects

See existing implementations:
- `manman/src/migrations/env.py` - Example with schema filtering
- `friendly_computing_machine/src/migrations/env.py` - Simple example
- `friendly_computing_machine/src/friendly_computing_machine/cli/migration_cli.py` - CLI integration

### Key Functions

**Configuration**:
```python
create_alembic_config(
    migrations_dir: str,
    database_url: str,
    file_template: Optional[str] = None,
    version_table_schema: Optional[str] = "public",
) -> alembic.config.Config
```

**Migration Operations**:
```python
run_migration(engine: Engine, config: alembic.config.Config) -> None
should_run_migration(engine: Engine, config: alembic.config.Config) -> bool
create_migration(engine: Engine, config: alembic.config.Config, message: Optional[str]) -> None
run_downgrade(engine: Engine, config: alembic.config.Config, revision: str) -> None
```

**Environment Module**:
```python
run_migrations(
    context: Any,
    target_metadata: Optional[MetaData],
    include_object: Optional[Callable] = None,
    include_schemas: bool = True,
    version_table_schema: Optional[str] = None,
) -> None
```

**CLI Framework**:
```python
create_migration_app(
    migrations_package: str,
    target_metadata: Optional[MetaData],
    include_object: Optional[Callable] = None,
    version_table_schema: str = "public",
) -> typer.Typer
```

The created app uses `@pg_params` decorator which automatically injects `POSTGRES_URL` environment variable.
