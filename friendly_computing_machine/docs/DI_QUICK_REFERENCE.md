# Dependency Injection Quick Reference

One-page reference for using the Typer dependency injection pattern.

## Quick Start

```python
from typing import Annotated
import typer
from friendly_computing_machine.cli.deps import Depends, inject_dependencies
from friendly_computing_machine.cli.injectable import get_db_context

app = typer.Typer(context_settings={"obj": {}})

@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    print(f"Database: {db_ctx.engine.url}")
```

## Available Dependencies

Import from `friendly_computing_machine.cli.injectable`:

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
get_manman_experience_api(manman_host_url: str) -> type
get_manman_status_api(manman_host_url: str) -> type

# RabbitMQ
get_rabbitmq_config(
    rabbitmq_host: str,
    rabbitmq_port: int = 5672,
    rabbitmq_user: str = None,
    rabbitmq_password: str = None,
    rabbitmq_enable_ssl: bool = False,
    rabbitmq_ssl_hostname: str = None,
    rabbitmq_vhost: str = "/",
) -> dict

# Gemini
get_gemini_config(google_api_key: str) -> bool
```

## Creating Custom Dependencies

```python
from friendly_computing_machine.cli.deps import injectable, Depends

@injectable
def get_my_service(
    ctx: typer.Context,
    api_key: Annotated[str, typer.Option(..., envvar="MY_API_KEY")],
    # Can depend on other dependencies
    config: Annotated[dict, Depends(get_config)],
) -> MyService:
    """Create and cache MyService instance."""
    if "my_service" not in ctx.obj:
        ctx.obj["my_service"] = MyService(api_key, config)
    return ctx.obj["my_service"]
```

## Usage Patterns

### Simple Injection
```python
@app.command()
@inject_dependencies
def cmd(ctx: typer.Context, db: Annotated[DBContext, Depends(get_db_context)]):
    pass
```

### Multiple Dependencies
```python
@app.command()
@inject_dependencies
def cmd(
    ctx: typer.Context,
    db: Annotated[DBContext, Depends(get_db_context)],
    env: Annotated[str, Depends(get_app_env)],
):
    pass
```

### Mixed with Regular Parameters
```python
@app.command()
@inject_dependencies
def cmd(
    ctx: typer.Context,
    db: Annotated[DBContext, Depends(get_db_context)],
    name: str = typer.Option("default"),
):
    pass
```

## Testing

### Mock a Dependency
```python
def test_my_command():
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    # Mock the dependency
    mock_db = Mock(spec=DBContext)
    dep = Depends(get_db_context)
    ctx.obj[dep.cache_key] = mock_db
    
    my_command(ctx)
```

### Test a Dependency Function
```python
def test_get_db_context():
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    result = get_db_context(ctx, database_url="sqlite:///test.db")
    
    assert isinstance(result, DBContext)
```

## Common Mistakes

❌ **Forgetting `@inject_dependencies`**
```python
@app.command()  # Missing decorator!
def cmd(ctx: typer.Context, db: Annotated[DBContext, Depends(get_db_context)]):
    pass  # Won't work - dependency not injected
```

✅ **Correct**
```python
@app.command()
@inject_dependencies  # Required!
def cmd(ctx: typer.Context, db: Annotated[DBContext, Depends(get_db_context)]):
    pass
```

---

❌ **Wrong type annotation**
```python
@inject_dependencies
def cmd(ctx: typer.Context, db: Depends(get_db_context)):  # Missing Annotated
    pass
```

✅ **Correct**
```python
@inject_dependencies
def cmd(ctx: typer.Context, db: Annotated[DBContext, Depends(get_db_context)]):
    pass
```

---

❌ **Not caching in custom dependency**
```python
@injectable
def get_expensive(ctx: typer.Context) -> object:
    return ExpensiveObject()  # Creates new instance every time!
```

✅ **Correct**
```python
@injectable
def get_expensive(ctx: typer.Context) -> object:
    if "expensive" not in ctx.obj:
        ctx.obj["expensive"] = ExpensiveObject()
    return ctx.obj["expensive"]
```

## Key Concepts

- **`Depends(func)`**: Marks a parameter for dependency injection
- **`@injectable`**: Optional decorator for dependency functions
- **`@inject_dependencies`**: Required decorator for command functions
- **Caching**: Dependencies resolved once, cached in `ctx.obj`
- **Chaining**: Dependencies can depend on other dependencies
- **Type Safety**: Full IDE autocomplete and type checking

## More Info

- Complete guide: `DEPENDENCY_INJECTION.md`
- Migration guide: `DI_MIGRATION_GUIDE.md`
- Examples: `example_cli.py`, `migration_cli_refactored.py`
- Tests: `tests/test_deps.py`
