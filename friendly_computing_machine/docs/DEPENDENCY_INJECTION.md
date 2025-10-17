# Typer Dependency Injection

A lightweight dependency injection system for Typer CLI applications that enables "define-once" reusable dependencies.

## Overview

The dependency injection (DI) system eliminates repetitive setup code in CLI applications by automatically resolving and injecting dependencies into command functions. Instead of manually setting up connections and configurations in callbacks, you define them once and the system injects them where needed.

**Key Benefits:**
- Define dependencies once, use everywhere
- Type-safe with full IDE autocomplete
- Automatic caching (dependencies resolved once per session)
- Easier testing with mock dependencies
- Cleaner, more maintainable code

## Using Existing Dependencies

### Basic Usage

Import the DI decorators from the shared library and injectable dependencies from your project:

```python
from typing import Annotated
import typer
from libs.python.cli.deps import Depends, inject_dependencies
from friendly_computing_machine.cli.injectable import get_db_context

app = typer.Typer(context_settings={"obj": {}})

@app.command()
@inject_dependencies  # Required decorator
def my_command(
    ctx: typer.Context,
    # Dependencies are automatically resolved and injected
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    # Regular CLI parameters still work
    name: str = "default",
):
    """Your command logic here."""
    print(f"Database: {db_ctx.engine.url}")
    print(f"Name: {name}")
```

### Available Dependencies (FCM)

The following pre-built dependencies are available in `friendly_computing_machine.cli.injectable`:

```python
# Logging
get_logging_config(log_otlp=False, log_console=False) -> dict

# Environment
get_app_env(app_env: str) -> str

# Database
get_db_context(database_url: str, echo=False) -> DBContext

# Slack
get_slack_tokens(slack_app_token: str, slack_bot_token: str) -> dict
get_slack_bot_token(slack_bot_token: str) -> dict

# Temporal
get_temporal_config(temporal_host: str, app_env: str) -> TemporalConfig

# ManMan APIs
get_manman_experience_api(manman_host_url: str) -> ManManExperienceAPI
get_manman_status_api(manman_host_url: str) -> ManManStatusAPI

# RabbitMQ
get_rabbitmq_config(rabbitmq_host: str, ...) -> dict

# Gemini
get_gemini_config(google_api_key: str) -> bool
```

### Example: Multiple Dependencies

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
    # Dependencies are automatically resolved
```

## Creating New Dependencies

### Simple Dependency

To create a new injectable dependency:

1. Import the `@injectable` decorator from the shared library
2. Create a function that takes `typer.Context` as first parameter
3. Add any additional parameters (with type annotations)
4. Cache the result in `ctx.obj` to avoid re-initialization
5. Return the dependency value

```python
from libs.python.cli.deps import injectable, Depends
from typing import Annotated
import typer

@injectable
def get_api_client(
    ctx: typer.Context,
    api_key: Annotated[str, typer.Option(..., envvar="API_KEY")],
    timeout: int = 30,
) -> MyAPIClient:
    """Get API client instance."""
    # Cache the client to avoid recreating it
    if "api_client" not in ctx.obj:
        ctx.obj["api_client"] = MyAPIClient(api_key=api_key, timeout=timeout)
    return ctx.obj["api_client"]
```

### Dependency with Other Dependencies

Dependencies can depend on other dependencies (dependency chaining):

```python
@injectable
def get_complex_service(
    ctx: typer.Context,
    # This dependency depends on get_db_context
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    api_key: Annotated[str, typer.Option(..., envvar="SERVICE_API_KEY")],
) -> ComplexService:
    """Service that needs database access."""
    if "complex_service" not in ctx.obj:
        ctx.obj["complex_service"] = ComplexService(
            engine=db_ctx.engine,
            api_key=api_key
        )
    return ctx.obj["complex_service"]
```

### Using Your New Dependency

Once defined, use it like any other dependency:

```python
@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    api_client: Annotated[MyAPIClient, Depends(get_api_client)],
    complex: Annotated[ComplexService, Depends(get_complex_service)],
):
    """Use your custom dependencies."""
    api_client.do_something()
    complex.process()
```

## Testing with Dependency Injection

Mock dependencies by pre-populating `ctx.obj` with the dependency's cache key:

```python
def test_my_command():
    from libs.python.cli.deps import Depends
    from unittest.mock import Mock
    
    # Create test context
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    # Mock the dependency
    mock_api = Mock(spec=MyAPIClient)
    dep = Depends(get_api_client)
    ctx.obj[dep.cache_key] = mock_api
    
    # Call your command
    my_command(ctx)
    
    # Verify mock was used
    mock_api.do_something.assert_called_once()
```

## Migration from Old Pattern

**Before (Manual Setup):**
```python
@app.callback()
def callback(ctx, db_url, slack_token, env):
    setup_db(ctx, db_url)
    setup_slack(ctx, slack_token)
    setup_app_env(ctx, env)

@app.command()
def cmd(ctx):
    db = ctx.obj[DB_FILENAME].engine  # String-based access
```

**After (Dependency Injection):**
```python
# No callback needed!

@app.command()
@inject_dependencies
def cmd(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    app_env: Annotated[str, Depends(get_app_env)],
):
    db = db_ctx.engine  # Type-safe access
```

## Best Practices

1. **Always cache dependencies** - Store in `ctx.obj` to avoid re-initialization
2. **Use type hints** - Enables IDE autocomplete and type checking
3. **Keep dependencies focused** - Each dependency should do one thing well
4. **Document parameters** - Add docstrings explaining what the dependency provides
5. **Test independently** - Write unit tests for dependency functions

## Common Patterns

### Environment-based Configuration
```python
@injectable
def get_config(
    ctx: typer.Context,
    app_env: Annotated[str, Depends(get_app_env)],
) -> Config:
    if "config" not in ctx.obj:
        ctx.obj["config"] = Config.load_for_env(app_env)
    return ctx.obj["config"]
```

### Shared Resources
```python
@injectable
def get_db_pool(
    ctx: typer.Context,
    database_url: Annotated[str, typer.Option(..., envvar="DATABASE_URL")],
) -> ConnectionPool:
    if "db_pool" not in ctx.obj:
        ctx.obj["db_pool"] = create_pool(database_url)
    return ctx.obj["db_pool"]
```

## Architecture

The DI system consists of two parts:

1. **Shared Library** (`//libs/python/cli`): Generic DI system that can be used by any Python project in the monorepo
2. **Project-Specific Dependencies** (e.g., `friendly_computing_machine.cli.injectable`): Pre-built dependencies specific to your project

This separation allows the core DI system to be reused while keeping project-specific implementations in their respective locations.

## Learn More

- **Shared Library**: `libs/python/cli/README.md` - Core DI system documentation
- **Example Code**: `friendly_computing_machine/cli/example_cli.py` - Working examples
- **Refactored Example**: `friendly_computing_machine/cli/migration_cli_refactored.py` - Real-world usage
