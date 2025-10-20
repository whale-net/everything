# CLI Library

Reusable CLI components for building type-safe, composable Typer applications with automatic parameter injection.

## Architecture

The library is organized into three layers:

### 1. Provider Modules (`libs/python/cli/providers/`)

Each provider module defines:
- **Type aliases**: `Annotated` types for Typer CLI parameters with environment variable support
- **Context classes**: Dataclasses holding the provider's resources
- **Factory functions**: Create context from parameters
- **Decorator**: Injects the provider's parameters into callbacks

Available providers:
- `rabbitmq.py` - RabbitMQ connections with SSL support
- `postgres.py` - PostgreSQL with Alembic integration
- `slack.py` - Slack Web API and Socket Mode clients
- `logging.py` - OpenTelemetry logging setup

### 2. Parameter Registry (`libs/python/cli/params.py`)

Central registry that:
- Exports `_create_param_decorator` factory used by all providers
- Re-exports type aliases from all providers for convenience
- Re-exports decorators from all providers

### 3. Combinators (`libs/python/cli/providers/combinators.py`)

High-level functions for project-specific initialization:
- FCM-specific setup functions (e.g., `setup_postgres_with_fcm_init`)
- Factory creators for building project-specific providers

## Quick Start

### Using Decorators (Recommended)

Decorators are defined in their provider modules but can be imported from params.py:

```python
from libs.python.cli.params import rmq_params, pg_params, slack_params, logging_params
import typer

app = typer.Typer()

@app.callback()
@rmq_params       # Injects 7 RabbitMQ parameters
@pg_params        # Injects 1 Postgres parameter
@slack_params     # Injects 2 Slack parameters
@logging_params   # Injects 1 logging parameter
def callback(ctx: typer.Context):
    # Parameters automatically stored in ctx.obj
    rmq = ctx.obj['rabbitmq']
    pg = ctx.obj['postgres']
    slack = ctx.obj['slack']
    logging = ctx.obj['logging']
```

### Decorator Details

#### `@rmq_params` (from `libs.python.cli.providers.rabbitmq`)
Injects 7 RabbitMQ parameters:
- `--rabbitmq-host` (envvar: RABBITMQ_HOST)
- `--rabbitmq-port` (envvar: RABBITMQ_PORT, default: 5672)
- `--rabbitmq-user` (envvar: RABBITMQ_USER, default: "guest")
- `--rabbitmq-password` (envvar: RABBITMQ_PASSWORD, default: "guest")
- `--rabbitmq-vhost` (envvar: RABBITMQ_VHOST, default: "/")
- `--rabbitmq-enable-ssl` (envvar: RABBITMQ_ENABLE_SSL, default: False)
- `--rabbitmq-ssl-hostname` (envvar: RABBITMQ_SSL_HOSTNAME, default: "")

Stores in `ctx.obj['rabbitmq']` as dict with keys: `host`, `port`, `user`, `password`, `vhost`, `enable_ssl`, `ssl_hostname`

#### `@pg_params` (from `libs.python.cli.providers.postgres`)
Injects 1 PostgreSQL parameter:
- `--database-url` (envvar: POSTGRES_URL, required)

Stores in `ctx.obj['postgres']` as dict with key: `database_url`

#### `@slack_params` (from `libs.python.cli.providers.slack`)
Injects 2 Slack parameters:
- `--slack-bot-token` (envvar: SLACK_BOT_TOKEN, required)
- `--slack-app-token` (envvar: SLACK_APP_TOKEN, optional, default: "")

Stores in `ctx.obj['slack']` as dict with keys: `bot_token`, `app_token`

#### `@logging_params` (from `libs.python.cli.providers.logging`)
Injects 1 logging parameter:
- `--log-otlp` (default: False)

Stores in `ctx.obj['logging']` as dict with key: `enable_otlp`

## Using Provider Functions

For more control, use provider factory functions directly:

```python
from libs.python.cli.providers.rabbitmq import RabbitMQHost, create_rabbitmq_context
from libs.python.cli.providers.postgres import PostgresUrl, create_postgres_context

@app.callback()
def callback(
    ctx: typer.Context,
    rabbitmq_host: RabbitMQHost,
    database_url: PostgresUrl,
):
    # Manual context creation
    ctx.obj = {
        'rabbitmq': create_rabbitmq_context(host=rabbitmq_host),
        'postgres': create_postgres_context(
            database_url=database_url,
            migrations_package="myapp.migrations"
        ),
    }
```

## Using Combinators

For project-specific initialization patterns:

```python
from libs.python.cli.providers.combinators import (
    setup_postgres_with_fcm_init,
    setup_slack_with_fcm_init,
)

@app.callback()
def callback(
    ctx: typer.Context,
    database_url: PostgresUrl,
    slack_bot_token: SlackBotToken,
):
    ctx.obj = {
        'postgres': setup_postgres_with_fcm_init(database_url),
        'slack': setup_slack_with_fcm_init(slack_bot_token),
    }
```

## Best Practices

1. **Stack decorators** for multiple services - cleaner than manual parameters
2. **Use combinators** for project-specific initialization (FCM, manman)
3. **Import from params.py** for convenience, or from provider modules directly
4. **All parameters remain visible** in `--help` output regardless of approach

## Migration Guide

### From Manual Parameters to Decorators

**Before** (12 parameters):
```python
@app.callback()
def callback(
    ctx: typer.Context,
    rabbitmq_host: RabbitMQHost = "localhost",
    rabbitmq_port: RabbitMQPort = 5672,
    rabbitmq_user: RabbitMQUser = "guest",
    rabbitmq_password: RabbitMQPassword = "guest",
    rabbitmq_vhost: RabbitMQVhost = "/",
    rabbitmq_enable_ssl: RabbitMQEnableSSL = False,
    rabbitmq_ssl_hostname: RabbitMQSSLHostname = "",
    slack_bot_token: SlackBotToken,
    slack_app_token: SlackAppToken = "",
    database_url: PostgresURL,
    log_otlp: EnableOTLP = False,
):
    # Manual context creation
    ...
```

**After** (1 parameter + decorators):
```python
@app.callback()
@rmq_params
@pg_params
@slack_params
@logging_params
def callback(ctx: typer.Context):
    # Parameters automatically in ctx.obj
    rmq = ctx.obj['rabbitmq']
    pg = ctx.obj['postgres']
    slack = ctx.obj['slack']
    logging = ctx.obj['logging']
```

**Result**: 92% parameter reduction, identical CLI help output

## Testing

When testing CLIs with decorators:

```python
from typer.testing import CliRunner

def test_cli():
    runner = CliRunner()
    result = runner.invoke(app, [
        "--rabbitmq-host", "localhost",
        "--database-url", "postgresql://localhost/test",
        "--slack-bot-token", "xoxb-test",
        "command",
    ], env={"POSTGRES_URL": "postgresql://localhost/test"})
    assert result.exit_code == 0
```

## Creating New Decorators

To add a new provider with decorator support:

1. Create provider module in `libs/python/cli/providers/your_provider.py`:

```python
import inspect
from typing import Annotated, Callable
import typer

# Define type aliases
YourParam = Annotated[str, typer.Option("--your-param", envvar="YOUR_PARAM")]

# Define context class and factory function
# ...

# Define decorator
def your_params(func: Callable) -> Callable:
    """Decorator that injects your parameters."""
    from libs.python.cli.params import _create_param_decorator
    
    param_specs = [
        ('your_param', inspect.Parameter(
            'your_param', inspect.Parameter.KEYWORD_ONLY,
            annotation=YourParam
        )),
    ]
    
    def extractor(kwargs):
        return {
            'param': kwargs.pop('your_param'),
        }
    
    return _create_param_decorator(param_specs, 'your_service', extractor)(func)
```

2. Re-export from `libs/python/cli/params.py`:

```python
from libs.python.cli.providers.your_provider import (
    YourParam,
    your_params,
)

__all__ = [
    ...
    'YourParam',
    'your_params',
]
```

## Troubleshooting

### CLI help doesn't show parameters
- Decorators must be applied AFTER `@app.callback()` or `@app.command()`
- Correct order: `@app.callback()` → `@decorator` → `def callback()`

### Type hints not working
- Import type aliases from `libs.python.cli.params` or provider modules
- Ensure `Annotated` types are used

### Context not available in commands
- Decorators only work on callbacks
- Commands access context via `ctx.obj` parameter

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
- `--database-url` (envvar: `POSTGRES_URL`, required)

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
