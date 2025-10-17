#!/usr/bin/env python3
"""
Demonstration script showing the dependency injection pattern.

This script demonstrates how the new dependency injection system works
without requiring any environment variables or external services.
"""

import logging
from typing import Annotated

import typer

from libs.python.cli.deps import (
    Depends,
    inject_dependencies,
    injectable,
)

# Configure logging
logging.basicConfig(level=logging.INFO, format="%(levelname)s: %(message)s")
logger = logging.getLogger(__name__)


# Define some example dependencies
@injectable
def get_config(ctx: typer.Context) -> dict:
    """Get application configuration."""
    if "config" not in ctx.obj:
        logger.info("🔧 Initializing configuration (expensive operation)...")
        ctx.obj["config"] = {
            "app_name": "Demo App",
            "version": "1.0.0",
            "environment": "development",
        }
    return ctx.obj["config"]


@injectable
def get_database(ctx: typer.Context) -> str:
    """Get database connection string."""
    if "database" not in ctx.obj:
        logger.info("🗄️  Connecting to database (expensive operation)...")
        ctx.obj["database"] = "postgresql://localhost/demo"
    return ctx.obj["database"]


@injectable
def get_api_client(
    ctx: typer.Context,
    config: Annotated[dict, Depends(get_config)],
) -> dict:
    """Get API client (depends on config)."""
    if "api_client" not in ctx.obj:
        logger.info("🌐 Initializing API client (depends on config)...")
        ctx.obj["api_client"] = {
            "base_url": f"https://api.{config['environment']}.example.com",
            "version": config["version"],
        }
    return ctx.obj["api_client"]


# Create the Typer app
app = typer.Typer(
    context_settings={"obj": {}},
    help="Demonstration of dependency injection pattern",
)


@app.command()
@inject_dependencies
def demo_basic(
    ctx: typer.Context,
    config: Annotated[dict, Depends(get_config)],
    database: Annotated[str, Depends(get_database)],
):
    """
    Basic example: inject multiple dependencies.
    
    Notice that get_config and get_database are only called once,
    even if used in multiple commands.
    """
    print("\n📦 Basic Dependency Injection Demo")
    print("=" * 50)
    print(f"App Name: {config['app_name']}")
    print(f"Version: {config['version']}")
    print(f"Environment: {config['environment']}")
    print(f"Database: {database}")
    print("✅ Dependencies injected successfully!\n")


@app.command()
@inject_dependencies
def demo_chained(
    ctx: typer.Context,
    api_client: Annotated[dict, Depends(get_api_client)],
):
    """
    Chained dependencies: api_client depends on config.
    
    Notice that get_config is called automatically to resolve api_client.
    """
    print("\n🔗 Chained Dependency Injection Demo")
    print("=" * 50)
    print(f"API Base URL: {api_client['base_url']}")
    print(f"API Version: {api_client['version']}")
    print("✅ Chained dependencies resolved successfully!\n")


@app.command()
@inject_dependencies
def demo_mixed(
    ctx: typer.Context,
    config: Annotated[dict, Depends(get_config)],
    name: str = typer.Option("Guest", help="Your name"),
    count: int = typer.Option(1, help="Number of greetings"),
):
    """
    Mixed parameters: both injected dependencies and regular CLI options.
    
    Shows that dependency injection works alongside normal Typer parameters.
    """
    print("\n🎭 Mixed Parameters Demo")
    print("=" * 50)
    print(f"Environment: {config['environment']}")
    
    for i in range(count):
        print(f"  {i + 1}. Hello, {name}!")
    
    print("✅ Mixed parameters work perfectly!\n")


@app.command()
@inject_dependencies
def demo_caching(
    ctx: typer.Context,
    config: Annotated[dict, Depends(get_config)],
    database: Annotated[str, Depends(get_database)],
    api_client: Annotated[dict, Depends(get_api_client)],
):
    """
    Caching demo: shows that dependencies are only initialized once.
    
    Even though we're using config, database, and api_client (which also
    depends on config), each initialization happens only once.
    """
    print("\n💾 Dependency Caching Demo")
    print("=" * 50)
    print("Notice the logs above - each dependency was only initialized once!")
    print(f"Config: {config['app_name']}")
    print(f"Database: {database}")
    print(f"API Client: {api_client['base_url']}")
    print("✅ Dependencies are cached and reused!\n")


@app.command()
def demo_all(ctx: typer.Context):
    """
    Run all demos in sequence.
    
    This demonstrates that dependencies are cached across multiple
    command invocations within the same session.
    """
    print("\n" + "=" * 50)
    print("🎯 Running All Demos")
    print("=" * 50)
    
    # Run each demo
    demo_basic(ctx)
    demo_chained(ctx)
    demo_mixed(ctx, name="Developer", count=2)
    demo_caching(ctx)
    
    print("=" * 50)
    print("✨ All demos completed!")
    print("=" * 50)
    print("\nKey Takeaways:")
    print("  1. Dependencies are defined once and reused everywhere")
    print("  2. Dependencies can depend on other dependencies")
    print("  3. All dependencies are automatically cached")
    print("  4. Regular CLI parameters work alongside injected dependencies")
    print("  5. No manual setup code needed in callbacks!")
    print()


if __name__ == "__main__":
    app()
