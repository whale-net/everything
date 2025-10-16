# Friendly Computing Machine CLI

Command-line interface for the Friendly Computing Machine application.

## Overview

This directory contains the CLI modules for FCM, including bot management, database migrations, workflow operations, and more. The CLI is built using [Typer](https://typer.tiangolo.com/) with a custom dependency injection system for clean, reusable code.

## Quick Start

### Basic Command Structure

All CLI modules follow a consistent pattern:

```python
from typing import Annotated

import typer

from friendly_computing_machine.cli.deps import Depends, inject_dependencies
from friendly_computing_machine.cli.injectable import get_db_context
from friendly_computing_machine.cli.context.db import DBContext

app = typer.Typer(context_settings={"obj": {}})

@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    # Your command logic here
    pass
```

## Dependency Injection System

FCM uses a custom dependency injection system inspired by FastAPI that makes CLI development cleaner and more maintainable.

### Key Features

- **Define Once, Use Everywhere**: Write dependency setup code once and inject it wherever needed
- **Type Safety**: Full type hints with IDE autocomplete support
- **Automatic Caching**: Dependencies are resolved once and cached for the session
- **Easy Testing**: Mock dependencies by pre-populating `ctx.obj`
- **No Callbacks**: No need for complex callback functions in most cases

### Documentation

- **[DEPENDENCY_INJECTION.md](../../docs/DEPENDENCY_INJECTION.md)** - Complete guide with examples
- **[DI_MIGRATION_GUIDE.md](../../docs/DI_MIGRATION_GUIDE.md)** - Before/after migration examples
- **[DI_QUICK_REFERENCE.md](../../docs/DI_QUICK_REFERENCE.md)** - One-page quick reference

### Examples

- **[example_cli.py](example_cli.py)** - Basic usage patterns
- **[demo_deps.py](demo_deps.py)** - Standalone demonstration
- **[migration_cli_refactored.py](migration_cli_refactored.py)** - Real refactored code

## Available CLI Modules

### Main Entry Point

- **[main.py](main.py)** - Main CLI entry point that combines all sub-commands

### Bot Management

- **[bot_cli.py](bot_cli.py)** - Slack bot operations
  - `run-taskpool` - Run the task pool service
  - `run-slack-socket-app` - Run the Slack socket app
  - `send-test-command` - Send a test message
  - `who-am-i` - Check bot identity

### Database Operations

- **[migration_cli.py](migration_cli.py)** - Database migration commands
  - `run` - Run pending migrations
  - `create` - Create a new migration
  - `downgrade` - Downgrade to a specific revision

- **[migration_cli_refactored.py](migration_cli_refactored.py)** - Same as above but using new DI pattern
  - Shows how to refactor existing code to use dependency injection

### Workflow Management

- **[workflow_cli.py](workflow_cli.py)** - Temporal workflow operations
  - `run` - Run the workflow worker

### Subscription Service

- **[subscribe_cli.py](subscribe_cli.py)** - ManMan subscribe service
  - `run` - Start the subscription service for RabbitMQ events

### Utilities

- **[tools_cli.py](tools_cli.py)** - Miscellaneous tools
  - `update-helm-chart-version` - Update Helm chart version (legacy)

## Context Modules

The `context/` directory contains setup functions for various dependencies:

- **[app_env.py](context/app_env.py)** - Application environment configuration
- **[db.py](context/db.py)** - Database engine and Alembic setup
- **[gemini.py](context/gemini.py)** - Google Gemini API setup
- **[log.py](context/log.py)** - Logging configuration
- **[manman_host.py](context/manman_host.py)** - ManMan API clients
- **[rabbitmq.py](context/rabbitmq.py)** - RabbitMQ connection setup
- **[slack.py](context/slack.py)** - Slack API clients
- **[temporal.py](context/temporal.py)** - Temporal client configuration

## Injectable Dependencies

The **[injectable.py](injectable.py)** module provides pre-built injectable dependencies for common use cases:

```python
from friendly_computing_machine.cli.injectable import (
    get_logging_config,      # Logging configuration
    get_app_env,            # Application environment
    get_db_context,         # Database context with engine and Alembic
    get_slack_tokens,       # Slack tokens (both app and bot)
    get_slack_bot_token,    # Slack bot token only
    get_temporal_config,    # Temporal client configuration
    get_manman_experience_api,  # ManMan Experience API
    get_manman_status_api,  # ManMan Status API
    get_rabbitmq_config,    # RabbitMQ configuration
    get_gemini_config,      # Gemini API configuration
)
```

## Usage Examples

### Example 1: Database Migration

```python
from typing import Annotated

import typer

from friendly_computing_machine.cli.deps import Depends, inject_dependencies
from friendly_computing_machine.cli.injectable import get_db_context
from friendly_computing_machine.cli.context.db import DBContext
from friendly_computing_machine.db.util import run_migration

app = typer.Typer(context_settings={"obj": {}})

@app.command()
@inject_dependencies
def migrate(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    """Run database migrations."""
    run_migration(db_ctx.engine, db_ctx.alembic_config)
```

### Example 2: Multiple Dependencies

```python
@app.command()
@inject_dependencies
def complex_command(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    app_env: Annotated[str, Depends(get_app_env)],
    slack: Annotated[dict, Depends(get_slack_tokens)],
):
    """Command using multiple dependencies."""
    print(f"Environment: {app_env}")
    print(f"Database: {db_ctx.engine.url}")
    print(f"Slack token: {slack['slack_bot_token'][:10]}...")
```

### Example 3: Custom Dependency

```python
from typing import Annotated

import typer

from friendly_computing_machine.cli.deps import injectable, Depends
from friendly_computing_machine.cli.injectable import get_db_context
from friendly_computing_machine.cli.context.db import DBContext

# Example custom service class
class MyService:
    def __init__(self, engine, api_key: str):
        self.engine = engine
        self.api_key = api_key
    
    def do_something(self):
        print("Service doing something...")

@injectable
def get_my_service(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    api_key: Annotated[str, typer.Option(..., envvar="MY_API_KEY")],
) -> MyService:
    """Create custom service with dependencies."""
    if "my_service" not in ctx.obj:
        ctx.obj["my_service"] = MyService(db_ctx.engine, api_key)
    return ctx.obj["my_service"]

@app.command()
@inject_dependencies
def use_service(
    ctx: typer.Context,
    service: Annotated[MyService, Depends(get_my_service)],
):
    """Use the custom service."""
    service.do_something()
```

## Testing

### Testing Commands

```python
def test_my_command():
    """Test a command with mocked dependencies."""
    from unittest.mock import Mock
    
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    # Mock the database dependency
    mock_db = Mock(spec=DBContext)
    dep = Depends(get_db_context)
    ctx.obj[dep.cache_key] = mock_db
    
    # Call the command
    my_command(ctx)
    
    # Verify the mock was used
    assert mock_db.engine.execute.called
```

### Testing Dependencies

```python
def test_get_db_context():
    """Test the database context dependency."""
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    result = get_db_context(ctx, database_url="sqlite:///test.db")
    
    assert isinstance(result, DBContext)
    assert str(result.engine.url) == "sqlite:///test.db"
```

## Migration from Old Pattern

If you have existing CLI code using the old callback pattern, see:

- **[DI_MIGRATION_GUIDE.md](../../docs/DI_MIGRATION_GUIDE.md)** - Detailed migration guide
- **[migration_cli_refactored.py](migration_cli_refactored.py)** - Example refactored code

### Quick Migration Steps

1. Import DI system:
   ```python
   from friendly_computing_machine.cli.deps import Depends, inject_dependencies
   from friendly_computing_machine.cli.injectable import get_db_context, ...
   ```

2. Add `@inject_dependencies` to commands:
   ```python
   @app.command()
   @inject_dependencies  # Add this
   def my_command(...):
   ```

3. Replace manual setup with injected dependencies:
   ```python
   # Before
   def my_command(ctx: typer.Context):
       db = ctx.obj[DB_FILENAME].engine
   
   # After
   def my_command(
       ctx: typer.Context,
       db_ctx: Annotated[DBContext, Depends(get_db_context)],
   ):
       db = db_ctx.engine
   ```

4. Remove or simplify callbacks (often not needed anymore)

## Architecture

### How It Works

1. **`Depends` class**: Wraps dependency functions and handles resolution
2. **`@inject_dependencies` decorator**: Inspects function signatures for `Depends` annotations
3. **Automatic resolution**: Calls dependency functions with required parameters
4. **Caching**: Stores resolved dependencies in `ctx.obj` with unique cache keys
5. **Recursive resolution**: Dependencies can depend on other dependencies

### Cache Keys

Dependencies are cached in `ctx.obj` using keys like:
```
__dep__friendly_computing_machine.cli.injectable.get_db_context
```

This ensures each dependency type has a unique cache entry.

## Best Practices

1. **Use type hints**: Always annotate return types for better IDE support
   ```python
   from typing import Annotated
   import typer
   
   @injectable
   def get_service(ctx: typer.Context) -> object:  # ← Include return type
       if "service" not in ctx.obj:
           ctx.obj["service"] = SomeService()
       return ctx.obj["service"]
   ```

2. **Cache expensive operations**: Store results in `ctx.obj`
   ```python
   from typing import Annotated
   import typer
   
   @injectable
   def get_expensive(ctx: typer.Context) -> object:
       if "expensive" not in ctx.obj:
           # Expensive initialization only happens once
           ctx.obj["expensive"] = create_expensive_object()
       return ctx.obj["expensive"]
   ```

3. **Keep dependencies focused**: Each dependency should do one thing well
   ```python
   # Good - focused on one concern
   @injectable
   def get_db_context(...) -> DBContext:
       ...
   
   # Bad - doing too much
   @injectable
   def get_everything(...) -> tuple:
       return (db, slack, api, config, ...)
   ```

4. **Document parameters**: Use docstrings
   ```python
   @injectable
   def get_db_context(
       ctx: typer.Context,
       database_url: T_database_url,
   ) -> DBContext:
       """
       Get database context with engine and Alembic configuration.
       
       Args:
           ctx: Typer context
           database_url: Database connection URL
           
       Returns:
           DBContext with engine and alembic_config
       """
   ```

5. **Test dependencies separately**: Write unit tests for dependency functions
   ```python
   def test_get_db_context():
       ctx = typer.Context(typer.Typer())
       ctx.obj = {}
       result = get_db_context(ctx, database_url="sqlite:///test.db")
       assert isinstance(result, DBContext)
   ```

## Troubleshooting

### Dependency Not Being Injected

**Problem**: Parameter value is `None` or using default

**Solution**: 
- Ensure `@inject_dependencies` decorator is present
- Check that parameter uses `Annotated[Type, Depends(func)]` syntax
- Verify dependency function is marked with `@injectable`

### Dependency Resolved Multiple Times

**Problem**: Initialization code runs on every call

**Solution**:
- Ensure you're caching the result in `ctx.obj`
- Check that dependency function checks for existing values before initializing

### Type Checking Errors

**Problem**: mypy or IDE complains about types

**Solution**:
- Use `Annotated[ActualType, Depends(func)]` not just `Depends(func)`
- Ensure dependency function has a return type annotation
- Import `Annotated` from `typing`

## Development

### Adding a New CLI Module

1. Create a new file in this directory (e.g., `my_new_cli.py`)
2. Define your Typer app:
   ```python
   import typer
   app = typer.Typer(context_settings={"obj": {}})
   ```
3. Add commands using the DI pattern
4. Register in `main.py`:
   ```python
   from friendly_computing_machine.cli.my_new_cli import app as my_app
   app.add_typer(my_app, name="my-command")
   ```

### Adding a New Injectable Dependency

1. Add to `injectable.py`:
   ```python
   @injectable
   def get_my_dependency(
       ctx: typer.Context,
       param: Annotated[str, typer.Option(..., envvar="MY_PARAM")],
   ) -> MyType:
       if "my_dep" not in ctx.obj:
           ctx.obj["my_dep"] = MyType(param)
       return ctx.obj["my_dep"]
   ```
2. Document in this README
3. Add tests in `tests/test_deps.py`

## See Also

- [Typer Documentation](https://typer.tiangolo.com/)
- [FastAPI Dependency Injection](https://fastapi.tiangolo.com/tutorial/dependencies/) (inspiration)
- [DEPENDENCY_INJECTION.md](../../docs/DEPENDENCY_INJECTION.md) - Complete DI guide
- [tests/test_deps.py](../../../tests/test_deps.py) - Test examples
