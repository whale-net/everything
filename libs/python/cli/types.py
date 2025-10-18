"""Base types and protocols for CLI providers."""

from typing import Protocol, TypeVar


class CLIContext(Protocol):
    """Protocol for CLI context objects.
    
    All context dataclasses should implicitly satisfy this protocol.
    This enables type-safe context passing without inheritance.
    """

    pass


# Generic type variable for contexts
TContext = TypeVar("TContext", bound=CLIContext)
