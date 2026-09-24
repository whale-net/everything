"""CLI subcommand that serves fcm's OIDC identity-link web app with uvicorn."""

import logging

import typer

from libs.python.cli.params import (
    logging_params,
    FcmWebPublicUrl,
    FcmOidcIssuerUrl,
    FcmOidcClientId,
    FcmOidcClientSecret,
    FcmWebSessionSecret,
)
from libs.python.cli.providers.app_env import app_env_params
from friendly_computing_machine.src.friendly_computing_machine.web.app import create_app
from friendly_computing_machine.src.friendly_computing_machine.web.config import WebConfig

logger = logging.getLogger(__name__)

app = typer.Typer(context_settings={"obj": {}})


@app.callback()
@logging_params
@app_env_params
def callback(ctx: typer.Context):
    logger.debug("web CLI callback starting")
    ctx.obj = ctx.obj if ctx.obj is not None else {}


@app.command("run")
def run(
    ctx: typer.Context,
    web_public_url: FcmWebPublicUrl,
    oidc_issuer_url: FcmOidcIssuerUrl,
    oidc_client_id: FcmOidcClientId,
    oidc_client_secret: FcmOidcClientSecret,
    web_session_secret: FcmWebSessionSecret,
    port: int = typer.Option(8000, envvar="FCM_WEB_PORT", help="Port to serve on"),
):
    """Run the OIDC identity-link web app."""
    import uvicorn

    config = WebConfig(
        public_url=web_public_url,
        oidc_issuer_url=oidc_issuer_url,
        oidc_client_id=oidc_client_id,
        oidc_client_secret=oidc_client_secret,
        session_secret=web_session_secret,
    )
    logger.info("starting fcm web identity-link app on port %d", port)
    uvicorn.run(create_app(config), host="0.0.0.0", port=port)
