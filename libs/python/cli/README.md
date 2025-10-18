# CLI Provider Library

Reusable CLI components for building typed, composable command-line interfaces with Typer.

## Overview

This library provides:
- **Parameter Decorators**: Stack decorators to inject service parameters into CLI callbacks
- **Provider Functions**: Create typed contexts for databases, message queues, logging, etc.
- **Combinators**: Project-specific initialization wrappers

## Quick Start

### Stackable Parameter Decorators

Reduce CLI callback signatures while keeping all parameters visible in `--help`:

```python
from libs.python.cli.params import rmq_params, pg_params, slack_params, logging_params
import typer

app = typer.Typer()

@app.callback()
@rmq_params      # Adds 7 RabbitMQ parameters
@pg_params       # Adds 1 PostgreSQL parameter
@slack_params    # Adds 2 Slack parameters
@logging_params  # Adds 1 logging parameter
def callback(
    ctx: typer.Context,
    # Only your app-specific params here!
    app_env: str = typer.Option(..., envvar="APP_ENV"),
):
    """Your CLI with automatic parameter injection."""
    
    # Access injected parameters from context
    rmq = ctx.obj['rabbitmq']
    pg = ctx.obj['postgres']
    slack = ctx.obj['slack']
    log_config = ctx.obj['logging']
    
    # Use provider functions to create contexts
    from libs.python.cli.providers.rabbitmq import create_rabbitmq_context
    rmq_ctx = create_rabbitmq_context(**rmq)
```

**Result**: Callback has 1 parameter instead of 11, but CLI still exposes all 11!

```bash
$ my-app --help
Options:
  --app-env TEXT                [required]
  --rabbitmq-host TEXT          [default: localhost]
  --rabbitmq-port INTEGER       [default: 5672]
  --rabbitmq-user TEXT          [default: guest]
  --rabbitmq-password TEXT      [default: guest]
  --rabbitmq-vhost TEXT         [default: /]
  --rabbitmq-enable-ssl
  --rabbitmq-ssl-hostname TEXT
  --database-url TEXT           [required]
  --slack-bot-token TEXT        [required]
  --slack-app-token TEXT
  --log-otlp                    Enable OTLP logging
```

## Available Decorators

### `@rmq_params` - RabbitMQ Connection

Injects 7 parameters into `ctx.obj['rabbitmq']`:

```python
@rmq_params
def callback(ctx: typer.Context, ...):
    rmq = ctx.obj['rabbitmq']
    # {'host': str, 'port': int, 'user': str, 'password': str,
    #  'vhost': str, 'enable_ssl': bool, 'ssl_hostname': str}
```

**CLI Options**:
- `--rabbitmq-host` (envvar: `RABBITMQ_HOST`, default: `localhost`)
- `--rabbitmq-port` (envvar: `RABBITMQ_PORT`, default: `5672`)
- `--rabbitmq-user` (envvar: `RABBITMQ_USER`, default: `guest`)
- `--rabbitmq-password` (envvar: `RABBITMQ_PASSWORD`, default: `guest`)
- `--rabbitmq-vhost` (envvar: `RABBITMQ_VHOST`, default: `/`)
- `--rabbitmq-enable-ssl` (envvar: `RABBITMQ_ENABLE_SSL`, default: `False`)
- `--rabbitmq-ssl-hostname` (envvar: `RABBITMQ_SSL_HOSTNAME`, default: `""`)

### `@pg_params` - PostgreSQL Database

Injects 1 parameter into `ctx.obj['postgres']`:

```python
@pg_params
def callback(ctx: typer.Context, ...):
    pg = ctx.obj['postgres']
    # {'database_url': str}
```

**CLI Options**:
- `--database-url` (envvar: `DATABASE_URL`, required)

### `@slack_params` - Slack Authentication

Injects 2 parameters into `ctx.obj['slack']`:

```python
@slack_params
def callback(ctx: typer.Context, ...):
    slack = ctx.obj['slack']
    # {'bot_token': str, 'app_token': str}
```

**CLI Options**:
- `--slack-bot-token` (envvar: `SLACK_BOT_TOKEN`, required)
- `--slack-app-token` (envvar: `SLACK_APP_TOKEN`, optional)

### `@logging_params` - Logging Configuration

Injects 1 parameter into `ctx.obj['logging']`:

```python
@logging_params
def callback(ctx: typer.Context, ...):
    log_config = ctx.obj['logging']
    # {'enable_otlp': bool}
```

**CLI Options**:
- `--log-otlp` (default: `False`, help: "Enable OTLP logging")

## Provider Functions

### PostgreSQL with Alembic

```python
from libs.python.cli.providers.postgres import create_postgres_context

@app.callback()
@pg_params
def callback(ctx: typer.Context):
    pg_config = ctx.obj['postgres']
    
    db = create_postgres_context(
        database_url=pg_config['database_url'],
        migrations_package="myapp.migrations",
        engine_initializer=None,  # Optional: custom engine setup
    )
    
    # db.engine: SQLAlchemy engine
    # db.alembic_config: Alembic configuration
    # db.url: Database URL
```

### RabbitMQ

```python
from libs.python.cli.providers.rabbitmq import create_rabbitmq_context

@app.callback()
@rmq_params
def callback(ctx: typer.Context):
    rmq_config = ctx.obj['rabbitmq']
    
    rmq = create_rabbitmq_context(**rmq_config)
    
    # rmq.host, rmq.port, rmq.user, etc.
```

### Slack

```python
from libs.python.cli.providers.slack import create_slack_context

@app.callback()
@slack_params
def callback(ctx: typer.Context):
    slack_config = ctx.obj['slack']
    
    slack = create_slack_context(
        bot_token=slack_config['bot_token'],
        app_token=slack_config.get('app_token', ''),
    )
    
    # slack.web_client: Slack WebClient
    # slack.socket_client: Optional SocketModeClient
```

### Logging with OpenTelemetry

```python
from libs.python.cli.providers.logging import create_logging_context

@app.callback()
@logging_params
def callback(ctx: typer.Context):
    log_config = ctx.obj['logging']
    
    create_logging_context(
        service_name="my-service",
        log_level="INFO",
        enable_otlp=log_config.get('enable_otlp', False),
    )
```

## Combinators (Project-Specific)

For project-specific initialization (e.g., FCM), use combinators:

```python
from libs.python.cli.providers.combinators import (
    setup_postgres_with_fcm_init,
    setup_slack_with_fcm_init,
    setup_rabbitmq_with_fcm_init,
)

@app.callback()
@pg_params
@slack_params
@rmq_params
def callback(ctx: typer.Context):
    # Automatically applies FCM-specific initialization
    db = setup_postgres_with_fcm_init(ctx.obj['postgres']['database_url'])
    slack = setup_slack_with_fcm_init(**ctx.obj['slack'])
    rmq = setup_rabbitmq_with_fcm_init(**ctx.obj['rabbitmq'])
```

## Architecture

### How Decorators Work

1. **Signature Injection**: Decorators use `inspect.signature()` to dynamically add parameters
2. **Parameter Extraction**: Wrapper function extracts injected params from `kwargs`
3. **Context Storage**: Extracted params stored in `ctx.obj[key]` as a dictionary
4. **Original Call**: Original function called with only its declared parameters

```python
# Before decoration
def callback(ctx: typer.Context, app_env: str):
    pass

# After @rmq_params decoration
def callback(
    ctx: typer.Context,
    app_env: str,
    rabbitmq_host: str = "localhost",  # Injected
    rabbitmq_port: int = 5672,         # Injected
    # ... 5 more injected params
):
    # Decorator extracts rabbitmq_* params
    # Stores in ctx.obj['rabbitmq']
    # Calls original callback(ctx, app_env)
    pass
```

### Type Safety

All parameters use `Annotated` types with Typer options:

```python
from typing import Annotated
import typer

RabbitMQHost = Annotated[str, typer.Option("--rabbitmq-host", envvar="RABBITMQ_HOST")]
```

This provides:
- IDE autocomplete
- Type checking
- Environment variable support
- Help text generation

## Best Practices

### 1. Stack Decorators in Dependency Order

```python
@app.callback()
@logging_params  # First: setup logging
@pg_params       # Second: database might log
@rmq_params      # Third: message queue might use db/logging
def callback(ctx: typer.Context, ...):
    pass
```

### 2. Use Context Objects

Store created contexts in `ctx.obj` for subcommands:

```python
@app.callback()
@pg_params
def callback(ctx: typer.Context):
    ctx.ensure_object(dict)
    ctx.obj['db_context'] = setup_postgres_with_fcm_init(
        ctx.obj['postgres']['database_url']
    )

@app.command()
def migrate(ctx: typer.Context):
    db = ctx.obj['db_context']
    # Use db for migrations
```

### 3. Handle Optional Configs

```python
@app.callback()
@rmq_params
def callback(ctx: typer.Context):
    rmq_config = ctx.obj.get('rabbitmq')
    if rmq_config:
        rmq_ctx = create_rabbitmq_context(**rmq_config)
    else:
        rmq_ctx = None
```

## Migration Guide

### From Manual Parameters

**Before**:
```python
def callback(
    ctx: typer.Context,
    database_url: str = typer.Option(..., envvar="DATABASE_URL"),
    rabbitmq_host: str = typer.Option("localhost", envvar="RABBITMQ_HOST"),
    rabbitmq_port: int = typer.Option(5672, envvar="RABBITMQ_PORT"),
    # ... 8 more params
):
    db = create_postgres_context(database_url, ...)
    rmq = create_rabbitmq_context(rabbitmq_host, rabbitmq_port, ...)
```

**After**:
```python
@pg_params
@rmq_params
def callback(ctx: typer.Context):
    db = create_postgres_context(**ctx.obj['postgres'])
    rmq = create_rabbitmq_context(**ctx.obj['rabbitmq'])
```

**Benefits**:
- 11 parameters → 1 parameter in function signature
- Same CLI interface (all 11 options still work)
- Easier to test (mock `ctx.obj` instead of 11 params)
- Reusable across multiple commands

## Extending

### Adding New Decorators

Create new parameter decorators in `params.py`:

```python
def temporal_params(func: Callable) -> Callable:
    """Inject Temporal workflow parameters."""
    param_specs = [
        ('temporal_host', inspect.Parameter(
            'temporal_host',
            inspect.Parameter.KEYWORD_ONLY,
            annotation=Annotated[str, typer.Option("--temporal-host", envvar="TEMPORAL_HOST")]
        )),
    ]
    
    def extractor(kwargs):
        return {'host': kwargs.pop('temporal_host')}
    
    return _create_param_decorator(param_specs, 'temporal', extractor)(func)
```

### Adding New Providers

Create provider functions in `providers/`:

```python
# libs/python/cli/providers/temporal.py
@dataclass
class TemporalContext:
    client: TemporalClient
    host: str

def create_temporal_context(host: str) -> TemporalContext:
    client = TemporalClient.connect(host)
    return TemporalContext(client=client, host=host)
```

## Testing

### Mocking Context

```python
import pytest
from typer.testing import CliRunner

def test_callback_with_mocked_params(mocker):
    runner = CliRunner()
    
    # Mock the decorator-injected params
    def mock_callback(ctx, **kwargs):
        ctx.ensure_object(dict)
        ctx.obj['rabbitmq'] = {'host': 'test-host', 'port': 5672, ...}
        return original_callback(ctx, **kwargs)
    
    result = runner.invoke(app, ['--app-env', 'test'])
    assert result.exit_code == 0
```

### Integration Testing

```python
def test_full_cli_integration():
    runner = CliRunner()
    result = runner.invoke(app, [
        '--database-url', 'postgresql://test',
        '--rabbitmq-host', 'localhost',
        '--slack-bot-token', 'xoxb-test',
        '--app-env', 'test',
    ])
    assert result.exit_code == 0
```

## Troubleshooting

### "Parameter X not found in context"

Ensure the decorator is applied:
```python
@app.callback()
@rmq_params  # ← Make sure this is here!
def callback(ctx: typer.Context):
    rmq = ctx.obj['rabbitmq']  # Will work now
```

### "Unexpected keyword argument"

Check decorator order - decorators run bottom-to-top:
```python
@app.callback()
@rmq_params      # Runs second (adds rabbitmq_* params)
@slack_params    # Runs first (adds slack_* params)
def callback(ctx: typer.Context):
    pass
```

### Type Errors in IDE

IDEs may not recognize dynamically added parameters. This is expected - the parameters exist at runtime and work correctly in Typer.

## References

- [Typer Documentation](https://typer.tiangolo.com/)
- [Python Decorators](https://docs.python.org/3/glossary.html#term-decorator)
- [inspect Module](https://docs.python.org/3/library/inspect.html)
