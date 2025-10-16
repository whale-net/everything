# Migration to Dependency Injection Pattern

This document shows practical examples of migrating existing Typer CLI code to use the new dependency injection pattern.

## Before and After Comparison

### Example 1: Migration CLI

**BEFORE (migration_cli.py):**
```python
import logging
from typing import Optional

import typer

from friendly_computing_machine.cli.context.db import (
    FILENAME as DB_FILENAME,
    T_database_url,
    setup_db,
)
from friendly_computing_machine.cli.context.log import setup_logging

logger = logging.getLogger(__name__)

migration_app = typer.Typer(context_settings={"obj": {}})


@migration_app.callback()
def callback(
    ctx: typer.Context,
    database_url: T_database_url,
    log_otlp: bool = False,
):
    """Required callback to set up dependencies."""
    logger.debug("CLI callback starting")
    setup_logging(ctx, log_otlp=log_otlp)
    setup_db(ctx, database_url)
    logger.debug("CLI callback complete")


@migration_app.command("run")
def cli_migration_run(ctx: typer.Context):
    """Run migrations - need to extract from ctx.obj."""
    logger.info("running migration")
    run_migration(
        ctx.obj[DB_FILENAME].engine,    # Manual extraction
        ctx.obj[DB_FILENAME].alembic_config
    )
    logger.info("migration complete")
```

**AFTER (migration_cli_refactored.py):**
```python
import logging
from typing import Annotated, Optional

import typer

from friendly_computing_machine.cli.context.db import DBContext
from friendly_computing_machine.cli.deps import Depends, inject_dependencies
from friendly_computing_machine.cli.injectable import get_db_context, get_logging_config

logger = logging.getLogger(__name__)

migration_app = typer.Typer(context_settings={"obj": {}})


# No callback needed!


@migration_app.command("run")
@inject_dependencies  # Enable automatic injection
def cli_migration_run(
    ctx: typer.Context,
    # Dependencies are automatically injected with type safety
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    log_config: Annotated[dict, Depends(get_logging_config)] = None,
):
    """Run migrations - dependencies automatically injected."""
    logger.info("running migration")
    run_migration(db_ctx.engine, db_ctx.alembic_config)  # Type-safe access
    logger.info("migration complete")
```

**Benefits:**
- ✅ No callback function needed
- ✅ No manual setup calls
- ✅ Type-safe access to dependencies
- ✅ IDE autocomplete works
- ✅ Easier to test (mock via ctx.obj)
- ✅ Less boilerplate code

---

### Example 2: Bot CLI

**BEFORE (bot_cli.py):**
```python
@app.callback()
def callback(
    ctx: typer.Context,
    slack_app_token: T_slack_app_token,
    slack_bot_token: T_slack_bot_token,
    temporal_host: T_temporal_host,
    app_env: T_app_env,
    manman_host_url: T_manman_host_url,
    log_otlp: bool = False,
):
    """Setup all dependencies in callback."""
    setup_logging(ctx, log_otlp=log_otlp)
    setup_slack(ctx, slack_app_token, slack_bot_token)
    setup_temporal(ctx, temporal_host, app_env)
    setup_manman_experience_api(ctx, manman_host_url)


@app.command("run-taskpool")
def cli_run_taskpool(
    ctx: typer.Context,
    database_url: T_database_url,
    skip_migration_check: bool = False,
):
    """Need extra setup in command."""
    setup_db(ctx, database_url)  # Additional setup
    
    if should_run_migration(
        ctx.obj[DB_FILENAME].engine,  # Manual extraction
        ctx.obj[DB_FILENAME].alembic_config
    ):
        raise RuntimeError("need to run migration")
    
    run_taskpool_only()
```

**AFTER:**
```python
# No callback needed!

@app.command("run-taskpool")
@inject_dependencies
def cli_run_taskpool(
    ctx: typer.Context,
    # All dependencies injected directly
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    slack: Annotated[dict, Depends(get_slack_tokens)],
    temporal: Annotated[TemporalConfig, Depends(get_temporal_config)],
    manman: Annotated[type, Depends(get_manman_experience_api)],
    skip_migration_check: bool = False,
):
    """All dependencies injected automatically."""
    if should_run_migration(db_ctx.engine, db_ctx.alembic_config):
        raise RuntimeError("need to run migration")
    
    run_taskpool_only()
```

**Benefits:**
- ✅ All dependencies in one place
- ✅ No duplicate setup code
- ✅ Clear dependency requirements
- ✅ Parameters only for what command actually uses

---

### Example 3: Subscribe CLI

**BEFORE (subscribe_cli.py):**
```python
@app.callback()
def callback(
    ctx: typer.Context,
    slack_bot_token: T_slack_bot_token,
    app_env: T_app_env,
    manman_host_url: T_manman_host_url,
    rabbitmq_host: T_rabbitmq_host,
    rabbitmq_port: T_rabbitmq_port = 5672,
    rabbitmq_user: T_rabbitmq_user = None,
    rabbitmq_password: T_rabbitmq_password = None,
    rabbitmq_enable_ssl: T_rabbitmq_enable_ssl = False,
    rabbitmq_ssl_hostname: T_rabbitmq_ssl_hostname = None,
    rabbitmq_vhost: T_rabbitmq_vhost = "/",
    log_otlp: bool = False,
):
    """Long callback with many parameters."""
    setup_logging(ctx, log_otlp=log_otlp)
    setup_app_env(ctx, app_env)
    setup_slack_web_client_only(ctx, slack_bot_token)
    setup_manman_status_api(ctx, manman_host_url)
    setup_rabbitmq(
        ctx,
        rabbitmq_host=rabbitmq_host,
        rabbitmq_port=rabbitmq_port,
        # ... many more parameters
    )


@app.command("run")
def cli_run(
    ctx: typer.Context,
    database_url: T_database_url,
    skip_migration_check: bool = False,
):
    """Still need more setup in command."""
    setup_db(ctx, database_url)
    
    if should_run_migration(
        ctx.obj[DB_FILENAME].engine,
        ctx.obj[DB_FILENAME].alembic_config
    ):
        raise RuntimeError("need to run migration")
    
    run_manman_subscribe(app_env=ctx.obj[APP_ENV_FILENAME]["app_env"])
```

**AFTER:**
```python
# No callback needed!

@app.command("run")
@inject_dependencies
def cli_run(
    ctx: typer.Context,
    # Only inject what this command needs
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    app_env: Annotated[str, Depends(get_app_env)],
    slack: Annotated[dict, Depends(get_slack_bot_token)],
    manman: Annotated[type, Depends(get_manman_status_api)],
    rabbitmq: Annotated[dict, Depends(get_rabbitmq_config)],
    skip_migration_check: bool = False,
):
    """All dependencies injected, no manual setup."""
    if should_run_migration(db_ctx.engine, db_ctx.alembic_config):
        raise RuntimeError("need to run migration")
    
    run_manman_subscribe(app_env=app_env)
```

**Benefits:**
- ✅ No massive callback with 11 parameters
- ✅ Dependencies declared per-command
- ✅ Only include what you need
- ✅ Self-documenting command signatures

---

## Migration Checklist

When converting an existing CLI module:

1. **Import the DI system:**
   ```python
   from friendly_computing_machine.cli.deps import Depends, inject_dependencies
   from friendly_computing_machine.cli.injectable import get_db_context, ...
   ```

2. **Remove or simplify callback:**
   - If callback only does setup, remove it entirely
   - If callback has business logic, keep it but remove setup calls

3. **Add `@inject_dependencies` to commands:**
   ```python
   @app.command()
   @inject_dependencies  # Add this
   def my_command(...):
   ```

4. **Replace parameters with dependencies:**
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

5. **Remove manual setup calls:**
   ```python
   # Before
   setup_db(ctx, database_url)
   
   # After
   # Nothing needed - automatic!
   ```

6. **Update ctx.obj access:**
   ```python
   # Before
   engine = ctx.obj[DB_FILENAME].engine
   
   # After
   engine = db_ctx.engine  # Direct access
   ```

---

## Gradual Migration Strategy

You don't need to migrate everything at once:

1. **Phase 1**: Add new commands using DI pattern
2. **Phase 2**: Refactor heavily-duplicated commands
3. **Phase 3**: Gradually convert remaining commands
4. **Phase 4**: Remove old callback pattern

Both patterns can coexist during migration!

---

## Testing Changes

### Before (manual setup):
```python
def test_my_command():
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    # Manually set up all dependencies
    setup_db(ctx, "sqlite:///test.db")
    setup_logging(ctx)
    
    my_command(ctx)
```

### After (mock dependencies):
```python
def test_my_command():
    ctx = typer.Context(typer.Typer())
    ctx.obj = {}
    
    # Mock just what you need
    mock_db = Mock(spec=DBContext)
    dep = Depends(get_db_context)
    ctx.obj[dep.cache_key] = mock_db
    
    my_command(ctx)
    
    # Verify mock was used
    mock_db.engine.execute.assert_called()
```

---

## Common Patterns

### Pattern 1: Database Commands
```python
@inject_dependencies
def db_command(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    # Work with db_ctx.engine and db_ctx.alembic_config
    pass
```

### Pattern 2: API Client Commands
```python
@inject_dependencies
def api_command(
    ctx: typer.Context,
    manman_api: Annotated[type, Depends(get_manman_experience_api)],
):
    # Use manman_api for API calls
    pass
```

### Pattern 3: Mixed Dependencies
```python
@inject_dependencies
def complex_command(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    app_env: Annotated[str, Depends(get_app_env)],
    slack: Annotated[dict, Depends(get_slack_tokens)],
    # Regular parameters still work
    count: int = typer.Option(1),
):
    # Use all dependencies
    pass
```

---

## See Also

- [DEPENDENCY_INJECTION.md](./DEPENDENCY_INJECTION.md) - Complete documentation
- [example_cli.py](../src/friendly_computing_machine/cli/example_cli.py) - Working examples
- [migration_cli_refactored.py](../src/friendly_computing_machine/cli/migration_cli_refactored.py) - Real refactored code
