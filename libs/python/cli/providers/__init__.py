"""CLI providers for common service dependencies.

Providers are factory functions that create typed context objects from CLI parameters.
Each provider module exports:
- Type aliases: Annotated types for Typer CLI parameters
- Context class: Dataclass holding the provider's resources
- Factory function: Creates context from parameters

Available providers:
- postgres: PostgreSQL database connections
- logging: Logging and OpenTelemetry setup
- slack: Slack Web API clients
- temporal: Temporal workflow clients
- rabbitmq: RabbitMQ connections
"""
