"""Base types and protocols for CLI providers."""

from typing import Annotated, Optional, Protocol, TypeVar

import typer


class CLIContext(Protocol):
    """Protocol for CLI context objects.
    
    All context dataclasses should implicitly satisfy this protocol.
    This enables type-safe context passing without inheritance.
    """

    pass


# Generic type variable for contexts
TContext = TypeVar("TContext", bound=CLIContext)


# ==============================================================================
# Common CLI parameter types
# ==============================================================================

# Application environment (dev, staging, prod, etc.)
AppEnv = Annotated[Optional[str], typer.Option(envvar="APP_ENV")]
