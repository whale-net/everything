"""FastAPI app for fcm's Slack -> Keycloak identity link flow.

The flow is: ``GET /link/{token}`` validates the one-time link token and
redirects to Keycloak; ``GET /link/callback`` completes the OIDC code exchange
and writes the Slack -> Keycloak mapping. Neither an access, refresh, nor ID
token is ever persisted or logged.
"""

import logging
from typing import Callable, ContextManager, Iterator, Optional

from authlib.integrations.starlette_client import OAuth
from fastapi import Depends, FastAPI
from fastapi.responses import HTMLResponse
from sqlmodel import Session
from starlette.middleware.sessions import SessionMiddleware
from starlette.requests import Request

from friendly_computing_machine.src.friendly_computing_machine.db.dal.identity_dal import (
    complete_link,
    peek_link_token,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.web.config import (
    WebConfig,
)

logger = logging.getLogger(__name__)

OAUTH_CLIENT_NAME = "keycloak"

# session key carrying the one-time link token across the Keycloak redirect.
# Authlib keeps its own OIDC state under separate keys in the same session.
LINK_TOKEN_SESSION_KEY = "fcm_link_token"


def _default_session_provider() -> ContextManager[Session]:
    """Yield a DB session backed by the process-wide engine."""
    return SessionManager()


SessionProvider = Callable[[], ContextManager[Session]]


def _html(title: str, body: str, status_code: int = 200) -> HTMLResponse:
    return HTMLResponse(
        f"<!doctype html><html><head><title>{title}</title></head>"
        f"<body><h1>{title}</h1><p>{body}</p></body></html>",
        status_code=status_code,
    )


def _error_page(title: str, body: str, status_code: int = 400) -> HTMLResponse:
    logger.info("identity link failed: %s (%s)", title, body)
    return _html(title, body, status_code)


def create_app(
    config: Optional[WebConfig] = None,
    session_provider: SessionProvider = _default_session_provider,
) -> FastAPI:
    """Build the FastAPI app.

    ``config`` overrides env sourcing; ``session_provider`` yields the DB
    session each request uses (defaults to the process-wide engine). Tests
    inject a SQLite-backed provider.
    """
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
    # stash the config + client the route handlers need without a second read
    app.state.config = config
    app.state.oauth = oauth

    # plain generator (not @contextmanager) so FastAPI runs it as a yield
    # dependency and resolves the Session, not the context-manager object.
    def _db_session() -> Iterator[Session]:
        with session_provider() as session:
            yield session

    @app.get("/health")
    async def health() -> HTMLResponse:
        return _html("ok", "healthy")

    @app.get("/link/callback")
    async def link_callback(
        request: Request, session: Session = Depends(_db_session)
    ) -> HTMLResponse:
        link_token = request.session.get(LINK_TOKEN_SESSION_KEY)
        if not link_token:
            return _error_page(
                "Link failed",
                "No pending link request found in this session. Start again from Slack.",
                status_code=400,
            )

        client = oauth.keycloak
        try:
            # Authlib verifies the ID token; we never hand-roll verification.
            token = await client.authorize_access_token(request)
        except Exception:
            logger.info("OIDC code exchange failed for link callback", exc_info=True)
            return _error_page(
                "Link failed",
                "Sign-in could not be completed. Nothing was linked. Start again from Slack.",
                status_code=400,
            )

        # iss/sub come from Authlib's verified ID-token claims.
        claims = token.get("userinfo") or {}
        keycloak_iss = claims.get("iss")
        keycloak_sub = claims.get("sub")
        if not keycloak_iss or not keycloak_sub:
            return _error_page(
                "Link failed",
                "Sign-in did not return a verified identity. Nothing was linked.",
                status_code=400,
            )

        identity = complete_link(link_token, keycloak_iss, keycloak_sub, session=session)
        if identity is None:
            # token unknown/expired/already consumed - a replay writes nothing.
            return _error_page(
                "Link failed",
                "This link request is no longer valid. Start again from Slack.",
                status_code=410,
            )

        # drop the link token + Authlib's OIDC state; the link is spent.
        request.session.clear()
        logger.info(
            "linked slack team=%s user=%s to keycloak iss=%s sub=%s",
            identity.slack_team_id,
            identity.slack_user_id,
            identity.keycloak_iss,
            identity.keycloak_sub,
        )
        return _html("Linked", "Your Slack account is now linked. Return to Slack.")

    @app.get("/link/{token}")
    async def link(
        token: str, request: Request, session: Session = Depends(_db_session)
    ):
        # peek only: the DB row stays the authority for the binding and the
        # token is consumed atomically in complete_link at the callback.
        if peek_link_token(token, session=session) is None:
            return _error_page(
                "Link failed",
                "This link is invalid or has expired. Start again from Slack.",
                status_code=400,
            )
        request.session[LINK_TOKEN_SESSION_KEY] = token
        return await oauth.keycloak.authorize_redirect(request, config.callback_url)

    return app


app = create_app()
