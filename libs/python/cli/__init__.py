"""Reusable CLI components with dependency injection pattern.

This package provides composable CLI building blocks that maintain Typer's
type hints while enabling code reuse across projects.

Key concepts:
- Type aliases: Reusable Annotated types for CLI parameters
- Context classes: Typed dataclasses for each provider
- Factory functions: Create contexts from CLI parameters
- Protocols: Define interfaces for providers
- Parameter packs: Group related parameters for clean passing

Example:
    ```python
    from libs.python.cli.providers.postgres import PostgresUrl
    from libs.python.cli.providers.combinators import setup_postgres_with_fcm_init
    from libs.python.cli.params import create_rabbitmq_from_params
    from dataclasses import dataclass
    import typer

    @dataclass
    class AppContext:
        db: DatabaseContext

    app = typer.Typer()

    @app.callback()
    def setup(
        ctx: typer.Context,
        database_url: PostgresUrl,
        rabbitmq_host: RabbitMQHost,
        rabbitmq_port: RabbitMQPort = 5672,
        # ... other rabbitmq params
    ):
        ctx.obj = AppContext(
            db=setup_postgres_with_fcm_init(database_url),
            rabbitmq=create_rabbitmq_from_params(locals()),  # Clean!
        )

    @app.command()
    def run(ctx: typer.Context):
        app_ctx: AppContext = ctx.obj
        engine = app_ctx.db.engine  # Type-safe!
    ```
"""

from libs.python.cli.types import CLIContext

__all__ = ["CLIContext"]
