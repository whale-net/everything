"""
Example CLI module demonstrating the dependency injection pattern.

This module shows how to use the new dependency injection system to simplify
CLI commands and avoid repetitive setup code.

Usage:
    # Run the example command
    bazel run //friendly_computing_machine/src/friendly_computing_machine/cli:example_cli -- \
        example command-with-deps \
        --database-url "sqlite:///test.db" \
        --app-env dev

The key benefits of this pattern:
    1. Define dependencies once, reuse everywhere
    2. Type-safe dependency resolution
    3. Automatic caching of resolved dependencies
    4. Cleaner command functions
    5. Easier testing with mock dependencies
"""

import logging
from typing import Annotated

import typer

from friendly_computing_machine.src.friendly_computing_machine.cli.context.db import (
    DBContext,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.deps import (
    Depends,
    inject_dependencies,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.injectable import (
    get_app_env,
    get_db_context,
    get_logging_config,
)

logger = logging.getLogger(__name__)

# Create the example app
app = typer.Typer(
    context_settings={"obj": {}},
    help="Example commands demonstrating dependency injection pattern",
)


# Example 1: Command with no callback required
# Dependencies are injected directly into the command function
@app.command("command-with-deps")
@inject_dependencies
def example_with_deps(
    ctx: typer.Context,
    # These dependencies are automatically resolved and injected
    app_env: Annotated[str, Depends(get_app_env)],
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
    log_config: Annotated[dict, Depends(get_logging_config)] = None,
    # Regular parameters still work as expected
    message: str = "Hello from dependency injection!",
):
    """
    Example command that uses dependency injection.
    
    This command demonstrates how dependencies are automatically resolved
    and injected into the command function. No manual callback setup needed!
    """
    logger.info(f"App environment: {app_env}")
    logger.info(f"Database engine: {db_ctx.engine}")
    logger.info(f"Logging configured: {log_config is not None}")
    logger.info(message)
    
    print(f"\n✓ Successfully executed with dependency injection!")
    print(f"  - Environment: {app_env}")
    print(f"  - Database URL: {db_ctx.engine.url}")
    print(f"  - Message: {message}")


# Example 2: Traditional pattern with callback (for comparison)
# This shows how the old pattern works for reference
@app.command("command-traditional")
def example_traditional(
    ctx: typer.Context,
    message: str = "Hello from traditional pattern!",
):
    """
    Example command using traditional pattern (for comparison).
    
    This command would typically require all dependencies to be set up
    in a callback function before the command runs.
    """
    # In the traditional pattern, you would access dependencies from ctx.obj
    # after they've been set up in a callback
    logger.info(message)
    print(f"\n✓ Successfully executed with traditional pattern!")
    print(f"  - Message: {message}")


# Example 3: Command that shares dependencies
# Multiple commands can use the same dependencies without duplication
@app.command("another-command")
@inject_dependencies
def another_example(
    ctx: typer.Context,
    # We can reuse the same dependencies in multiple commands
    app_env: Annotated[str, Depends(get_app_env)],
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    """
    Another command demonstrating dependency reuse.
    
    This command uses the same dependencies as the first example,
    but they're automatically resolved and cached - no duplication needed!
    """
    logger.info(f"Reusing environment: {app_env}")
    logger.info(f"Reusing database: {db_ctx.engine}")
    
    print(f"\n✓ Dependencies are shared and cached!")
    print(f"  - Environment: {app_env}")
    print(f"  - Database: {db_ctx.engine.url}")


# Example 4: Command with mixed dependencies and parameters
@app.command("mixed-params")
@inject_dependencies
def mixed_example(
    ctx: typer.Context,
    # Injected dependency
    app_env: Annotated[str, Depends(get_app_env)],
    # Regular CLI parameters
    count: int = typer.Option(1, help="Number of iterations"),
    name: str = typer.Option("user", help="User name"),
):
    """
    Example with both injected dependencies and regular CLI parameters.
    
    This shows that dependency injection works alongside normal Typer
    options and arguments without any conflicts.
    """
    logger.info(f"Environment: {app_env}, User: {name}, Count: {count}")
    
    print(f"\n✓ Mixed parameters work great!")
    print(f"  - Environment: {app_env}")
    print(f"  - User: {name}")
    print(f"  - Count: {count}")
    
    for i in range(count):
        print(f"  - Iteration {i + 1}")


# Example 5: Testing helper - shows how to test with dependency injection
@app.command("test-mode")
@inject_dependencies
def test_mode_example(
    ctx: typer.Context,
    db_ctx: Annotated[DBContext, Depends(get_db_context)],
):
    """
    Example showing how dependency injection makes testing easier.
    
    With dependency injection, you can easily mock dependencies in tests
    by pre-populating ctx.obj before calling the command function.
    """
    # In tests, you would mock the dependency like this:
    # ctx.obj["__dep__get_db_context"] = mock_db_context
    # Then call the function normally
    
    logger.info(f"Testing with database: {db_ctx.engine}")
    print(f"\n✓ Testing with dependency injection is easy!")
    print(f"  - Just mock ctx.obj entries")
    print(f"  - Database: {db_ctx.engine.url}")


if __name__ == "__main__":
    app()
