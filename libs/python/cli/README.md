# Python CLI Utilities

Reusable CLI utilities for Python applications using Typer.

## Contents

### Dependency Injection System (`deps.py`)

A lightweight dependency injection system for Typer CLI applications that enables "define-once" reusable dependencies.

**Key Features:**
- Define dependencies once, reuse everywhere
- Type-safe dependency resolution with full IDE autocomplete
- Automatic dependency injection and caching
- Easier testing with mock dependencies
- Cleaner command functions

**Basic Usage:**

```python
from typing import Annotated
import typer
from libs.python.cli.deps import Depends, inject_dependencies, injectable

@injectable
def get_database(
    ctx: typer.Context,
    database_url: Annotated[str, typer.Option(..., envvar="DATABASE_URL")],
) -> Engine:
    if "db_engine" not in ctx.obj:
        ctx.obj["db_engine"] = create_engine(database_url)
    return ctx.obj["db_engine"]

app = typer.Typer(context_settings={"obj": {}})

@app.command()
@inject_dependencies
def my_command(
    ctx: typer.Context,
    db: Annotated[Engine, Depends(get_database)],
):
    # db is automatically resolved and injected
    print(f"Using database: {db}")
```

## Documentation

For complete documentation on the dependency injection system, see:

- **Complete Guide**: `docs/DEPENDENCY_INJECTION.md`
- **Quick Reference**: `docs/DI_QUICK_REFERENCE.md`
- **Migration Guide**: `docs/DI_MIGRATION_GUIDE.md`
- **Implementation Summary**: `docs/DI_IMPLEMENTATION_SUMMARY.md`

## Usage in Projects

### Bazel

Add to your `BUILD.bazel`:

```python
py_library(
    name = "my_cli",
    srcs = ["my_cli.py"],
    deps = [
        "//libs/python/cli",
        "@pypi//typer",
    ],
)
```

### Python Imports

```python
from libs.python.cli import Depends, inject_dependencies, injectable
# or
from libs.python.cli.deps import Depends, inject_dependencies, injectable
```

## Benefits

1. **Reduced Boilerplate**: Eliminate repetitive callback setup code
2. **Type Safety**: Full type hints with IDE autocomplete support
3. **Better Testing**: Easy mocking via `ctx.obj`
4. **Self-Documenting**: Command signatures clearly show dependencies
5. **Reusability**: Share dependencies across multiple CLI modules
6. **Maintainability**: Single source of truth for dependency setup

## Examples

See the `friendly_computing_machine/src/friendly_computing_machine/cli/` directory for examples:

- `example_cli.py` - Demonstrates all usage patterns
- `demo_deps.py` - Standalone demonstration
- `migration_cli_refactored.py` - Real-world refactored code

## Testing

Tests for the dependency injection system are in `friendly_computing_machine/tests/test_deps.py`.

Run tests with:
```bash
bazel test //friendly_computing_machine/tests:test_deps
```
