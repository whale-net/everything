# Typer Dependency Injection

A lightweight dependency injection system for Typer CLI applications that enables "define-once" reusable dependencies.

## Overview

This dependency injection system solves a common problem in Typer applications: repetitive setup code across multiple CLI commands. Instead of manually calling setup functions in callbacks and passing parameters around, you can define dependencies once and inject them automatically.

## Key Benefits

1. **Define Once, Use Everywhere**: Write dependency setup code once and reuse it across all commands
2. **Type Safety**: Full type hints and IDE autocomplete support
3. **Automatic Caching**: Dependencies are resolved once and cached for the session
4. **Clean Code**: Commands focus on business logic, not setup boilerplate
5. **Easy Testing**: Mock dependencies by pre-populating `ctx.obj`

## Quick Start

### Basic Usage

```python
from typing import Annotated
import typer
from friendly_computing_machine.cli.deps import Depends, inject_dependencies
from friendly_computing_machine.cli.injectable import get_db_context, get_app_env

app = typer.Typer(context_settings={"obj": {}})

@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    # Dependencies are automatically resolved and injected
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    app_env: Annotated[str, Depends(get_app_env)],
    # Regular parameters work as usual
    name: str = "default",
):
    """Your command logic here."""
    print(f"Environment: {app_env}")
    print(f"Database: {db_ctx.engine.url}")
    print(f"Name: {name}")
```

### Comparison: Old vs New Pattern

**Old Pattern (Manual Setup)**:
```python
@app.callback()
def callback(
    ctx: typer.Context,
    database_url: Annotated[str, typer.Option(..., envvar="DATABASE_URL")],
    app_env: Annotated[str, typer.Option(..., envvar="APP_ENV")],
    slack_token: Annotated[str, typer.Option(..., envvar="SLACK_TOKEN")],
    # ... many more parameters
):
    setup_logging(ctx)
    setup_db(ctx, database_url)
    setup_app_env(ctx, app_env)
    setup_slack(ctx, slack_token)
    # ... many more setup calls

@app.command()
def my_command(
    ctx: typer.Context,
    # Need to extract from ctx.obj
):
    db = ctx.obj["db"]
    env = ctx.obj["app_env"]
    # ... manual extraction
```

**New Pattern (Dependency Injection)**:
```python
# No callback needed!

@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    app_env: Annotated[str, Depends(get_app_env)],
):
    # Dependencies are automatically resolved and available
    print(f"Environment: {app_env}")
    print(f"Database: {db_ctx.engine.url}")
```

## Core Concepts

### 1. Injectable Dependencies

Mark functions as injectable dependencies using the `@injectable` decorator:

```python
from friendly_computing_machine.cli.deps import injectable

@injectable
def get_database(
    ctx: typer.Context,
    database_url: Annotated[str, typer.Option(..., envvar="DATABASE_URL")],
) -> Engine:
    """Get database engine."""
    if "db_engine" not in ctx.obj:
        ctx.obj["db_engine"] = create_engine(database_url)
    return ctx.obj["db_engine"]
```

### 2. Depends Class

Use `Depends` to mark parameters that should be automatically resolved:

```python
from friendly_computing_machine.cli.deps import Depends

@app.command()
def my_command(
    ctx: typer.Context,
    db: Annotated[Engine, Depends(get_database)],  # Automatically resolved
):
    print(f"Database: {db.url}")
```

### 3. inject_dependencies Decorator

Apply the `@inject_dependencies` decorator to command functions:

```python
from friendly_computing_machine.cli.deps import inject_dependencies

@app.command()
@inject_dependencies  # Required for automatic injection
def my_command(
    ctx: typer.Context,
    db: Annotated[Engine, Depends(get_database)],
):
    # Dependencies are resolved before this function runs
    pass
```

## Advanced Features

### Dependency Chaining

Dependencies can depend on other dependencies:

```python
@injectable
def get_app_env(
    ctx: typer.Context,
    app_env: Annotated[str, typer.Option(..., envvar="APP_ENV")],
) -> str:
    return app_env

@injectable
def get_temporal_config(
    ctx: typer.Context,
    temporal_host: Annotated[str, typer.Option(..., envvar="TEMPORAL_HOST")],
    app_env: Annotated[str, Depends(get_app_env)],  # Depends on another dependency
) -> TemporalConfig:
    """Temporal config depends on app_env."""
    return TemporalConfig(host=temporal_host, env=app_env)

@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    temporal: Annotated[TemporalConfig, Depends(get_temporal_config)],
):
    # Both get_app_env and get_temporal_config are resolved automatically
    print(f"Temporal: {temporal.host} in {temporal.env}")
```

### Automatic Caching

Dependencies are resolved once and cached for the entire session:

```python
@injectable
def expensive_setup(ctx: typer.Context) -> Connection:
    """This expensive operation only runs once."""
    print("Initializing connection...")
    return create_connection()

@app.command()
@inject_dependencies
def command1(
    ctx: typer.Context,
    conn: Annotated[Connection, Depends(expensive_setup)],
):
    print("Command 1")  # "Initializing connection..." printed

@app.command()
@inject_dependencies
def command2(
    ctx: typer.Context,
    conn: Annotated[Connection, Depends(expensive_setup)],
):
    print("Command 2")  # Uses cached connection, no "Initializing..." message
```

### Mixed Parameters

Regular Typer parameters work alongside injected dependencies:

```python
@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    # Injected dependencies
    db: Annotated[Engine, Depends(get_database)],
    app_env: Annotated[str, Depends(get_app_env)],
    # Regular Typer options/arguments
    count: int = typer.Option(1, help="Number of iterations"),
    name: str = typer.Option("user", help="User name"),
    output: str = typer.Argument(..., help="Output file"),
):
    """All parameter types work together."""
    print(f"Environment: {app_env}")
    print(f"Database: {db.url}")
    print(f"Processing {count} iterations for {name}")
    print(f"Output to: {output}")
```

## Available Injectable Dependencies

The `friendly_computing_machine.cli.injectable` module provides pre-built injectable dependencies:

```python
from friendly_computing_machine.cli.injectable import (
    get_logging_config,      # Logging configuration
    get_app_env,            # Application environment (dev/staging/prod)
    get_db_context,         # Database context with engine and Alembic config
    get_slack_tokens,       # Slack tokens (both app and bot)
    get_slack_bot_token,    # Slack bot token only
    get_temporal_config,    # Temporal client configuration
    get_manman_experience_api,  # ManMan Experience API client
    get_manman_status_api,  # ManMan Status API client
    get_rabbitmq_config,    # RabbitMQ configuration
    get_gemini_config,      # Gemini API configuration
)
```

## Testing with Dependency Injection

### Mocking Dependencies

You can easily mock dependencies for testing by pre-populating `ctx.obj`:

```python
def test_my_command():
    """Test a command with mocked dependencies."""
    # Create test context
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    # Mock the dependency
    mock_db = Mock(spec=Engine)
    dep = Depends(get_database)
    ctx.obj[dep.cache_key] = mock_db
    
    # Call the command
    my_command(ctx, name="test")
    
    # Verify mock was used
    mock_db.some_method.assert_called_once()
```

### Testing Dependency Functions

Test dependency functions directly:

```python
def test_get_database():
    """Test the database dependency."""
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    result = get_database(ctx, database_url="sqlite:///test.db")
    
    assert isinstance(result, Engine)
    assert str(result.url) == "sqlite:///test.db"
```

## Creating Custom Dependencies

### Simple Dependency

```python
from friendly_computing_machine.cli.deps import injectable

@injectable
def get_my_service(
    ctx: typer.Context,
    api_key: Annotated[str, typer.Option(..., envvar="MY_API_KEY")],
) -> MyService:
    """Get MyService instance."""
    if "my_service" not in ctx.obj:
        ctx.obj["my_service"] = MyService(api_key=api_key)
    return ctx.obj["my_service"]
```

### Dependency with Other Dependencies

```python
@injectable
def get_complex_service(
    ctx: typer.Context,
    db: Annotated[Engine, Depends(get_database)],
    config: Annotated[dict, Depends(get_config)],
    timeout: int = 30,
) -> ComplexService:
    """Get ComplexService that depends on database and config."""
    return ComplexService(db=db, config=config, timeout=timeout)
```

## Migration Guide

### Migrating Existing CLI Modules

1. **Remove or simplify callbacks**: You may not need them anymore
2. **Add `@inject_dependencies`**: Add to command functions
3. **Replace `ctx.obj` access**: Use injected dependencies instead
4. **Use pre-built dependencies**: Import from `injectable` module

**Before**:
```python
@app.callback()
def callback(ctx: typer.Context, database_url: T_database_url):
    setup_db(ctx, database_url)

@app.command()
def my_command(ctx: typer.Context):
    engine = ctx.obj[DB_FILENAME].engine
    print(f"Database: {engine.url}")
```

**After**:
```python
# No callback needed!

@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    print(f"Database: {db_ctx.engine.url}")
```

## Examples

See `example_cli.py` for complete working examples demonstrating:
- Basic dependency injection
- Multiple dependencies
- Mixed dependencies and regular parameters
- Dependency chaining
- Testing patterns

Run examples:
```bash
# Example with dependencies
bazel run //friendly_computing_machine/src/friendly_computing_machine/cli:example_cli -- \
    command-with-deps \
    --database-url "sqlite:///test.db" \
    --app-env dev

# Example with mixed parameters
bazel run //friendly_computing_machine/src/friendly_computing_machine/cli:example_cli -- \
    mixed-params \
    --app-env dev \
    --count 3 \
    --name "alice"
```

## Architecture

### How It Works

1. **`Depends` class**: Wraps a dependency function and provides resolution logic
2. **`@inject_dependencies` decorator**: Inspects function signature for `Depends` annotations
3. **Automatic resolution**: Calls dependency functions with required parameters
4. **Caching**: Stores resolved dependencies in `ctx.obj` with unique cache keys
5. **Recursive resolution**: Dependencies can depend on other dependencies

### Cache Keys

Dependencies are cached using keys like:
```
__dep__module_name.function_name
```

This ensures each dependency type has a unique cache entry and is only resolved once per CLI session.

## Best Practices

1. **Use type hints**: Always annotate return types for better IDE support
2. **Cache expensive operations**: Store results in `ctx.obj` to avoid re-initialization
3. **Keep dependencies focused**: Each dependency should do one thing well
4. **Document parameters**: Use docstrings to explain what each dependency provides
5. **Test dependencies separately**: Write unit tests for dependency functions

## Troubleshooting

### Dependency Not Being Injected

**Problem**: Parameter is not being resolved automatically

**Solution**: 
- Ensure you've added the `@inject_dependencies` decorator
- Check that the parameter uses `Annotated[Type, Depends(func)]` syntax
- Verify the dependency function is marked with `@injectable`

### Dependency Resolved Multiple Times

**Problem**: Initialization code runs multiple times

**Solution**:
- Check that you're caching the result in `ctx.obj`
- Ensure the dependency function checks for existing values before initializing

### Type Checking Errors

**Problem**: mypy or IDE complains about types

**Solution**:
- Use `Annotated[ActualType, Depends(func)]` not just `Depends(func)`
- Ensure dependency function has a return type annotation
- Import `Annotated` from `typing`

## Further Reading

- [Typer Documentation](https://typer.tiangolo.com/)
- [FastAPI Dependency Injection](https://fastapi.tiangolo.com/tutorial/dependencies/) (inspiration for this system)
- Python Type Hints: [PEP 593 - Annotated](https://www.python.org/dev/peps/pep-0593/)
