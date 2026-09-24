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

# whagent-net client (fcm -> whagent_net's api SessionService, service account)
WhagentApiUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="WHAGENT_API_URL",
        help="gRPC address for whagent-net's api SessionService (host:port)"
    )
]

WhagentUiPublicUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="WHAGENT_UI_PUBLIC_URL",
        help="Public base URL for whagent-net's ui, used to build session links (e.g. https://whagent.example.com)"
    )
]

WhagentKeycloakTokenUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="WHAGENT_KEYCLOAK_TOKEN_URL",
        help="Keycloak token endpoint for fcm's whagent-net service-account client_credentials grant"
    )
]

WhagentClientId = Annotated[
    str,
    typer.Option(
        ...,
        envvar="WHAGENT_CLIENT_ID",
        help="Keycloak client id for fcm's whagent-net service account"
    )
]

WhagentClientSecret = Annotated[
    str,
    typer.Option(
        ...,
        envvar="WHAGENT_CLIENT_SECRET",
        help="Keycloak client secret for fcm's whagent-net service account"
    )
]

# fcm web (Slack -> Keycloak identity link endpoint, browser OIDC login)
FcmWebPublicUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="FCM_WEB_PUBLIC_URL",
        help="Externally-reachable base URL of fcm's web app, used to build the OIDC callback URL (e.g. https://fcm-web.example.com)"
    )
]

FcmOidcIssuerUrl = Annotated[
    str,
    typer.Option(
        ...,
        envvar="FCM_OIDC_ISSUER_URL",
        help="Keycloak realm issuer URL for the browser-login OIDC client"
    )
]

FcmOidcClientId = Annotated[
    str,
    typer.Option(
        ...,
        envvar="FCM_OIDC_CLIENT_ID",
        help="Keycloak client id for the confidential browser-login client"
    )
]

FcmOidcClientSecret = Annotated[
    str,
    typer.Option(
        ...,
        envvar="FCM_OIDC_CLIENT_SECRET",
        help="Keycloak client secret for the confidential browser-login client"
    )
]

FcmWebSessionSecret = Annotated[
    str,
    typer.Option(
        ...,
        envvar="FCM_WEB_SESSION_SECRET",
        help="Signing key for the Starlette session cookie"
    )
]
