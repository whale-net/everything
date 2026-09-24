"""FastAPI app for fcm's Slack -> Keycloak identity link flow.

The flow is: ``GET /link/{token}`` validates the one-time link token and
redirects to Keycloak; ``GET /link/callback`` completes the OIDC code exchange
and writes the Slack -> Keycloak mapping. Neither an access, refresh, nor ID
token is ever persisted or logged.
"""

import logging

from authlib.integrations.starlette_client import OAuth
from fastapi import FastAPI
from fastapi.responses import HTMLResponse
from starlette.middleware.sessions import SessionMiddleware

from friendly_computing_machine.src.friendly_computing_machine.web.config import (
    WebConfig,
)

logger = logging.getLogger(__name__)

OAUTH_CLIENT_NAME = "keycloak"


def create_app(config: WebConfig | None = None) -> FastAPI:
    """Build the FastAPI app. Pass a ``WebConfig`` to override env sourcing."""
    config = config or WebConfig.from_env()

    app = FastAPI(title="FCM Identity Link")
    app.add_middleware(
        SessionMiddleware, secret_key=config.session_secret or "insecure-dev-secret"
    )

    oauth = OAuth()
    oauth.register(
        name=OAUTH_CLIENT_NAME,
        client_id=config.oidc_client_id,
        client_secret=config.oidc_client_secret,
        server_metadata_url=f"{config.oidc_issuer_url.rstrip('/')}"
        "/.well-known/openid-configuration",
        client_kwargs={"scope": "openid email profile"},
    )
    # stash the config the route handlers need without a second env read
    app.state.config = config
    app.state.oauth = oauth

    @app.get("/health")
    async def health() -> HTMLResponse:
        return HTMLResponse("ok")

    return app


app = create_app()
