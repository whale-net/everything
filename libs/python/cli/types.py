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

# ManMan service host URL (DEPRECATED - use individual API URLs instead)
# Kept for backward compatibility
ManManHostUrl = Annotated[str, typer.Option(..., envvar="MANMAN_HOST_URL", help="Deprecated: use MANMAN_EXPERIENCE_API_URL, MANMAN_STATUS_API_URL, MANMAN_WORKER_DAL_API_URL instead")]

# ManMan API URLs (separate for each service due to split ingresses)
ManManExperienceApiUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="MANMAN_EXPERIENCE_API_URL",
        help="URL for ManMan Experience API (e.g., http://experience-api.manman.svc.cluster.local)"
    )
]

ManManStatusApiUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="MANMAN_STATUS_API_URL",
        help="URL for ManMan Status API (e.g., http://status-api.manman.svc.cluster.local)"
    )
]

ManManWorkerDalApiUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="MANMAN_WORKER_DAL_API_URL",
        help="URL for ManMan Worker DAL API (e.g., http://worker-dal-api.manman.svc.cluster.local)"
    )
]
