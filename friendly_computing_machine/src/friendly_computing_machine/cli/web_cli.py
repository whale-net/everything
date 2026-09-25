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
from libs.python.cli.providers.postgres import create_postgres_context
from libs.python.cli.providers.postgres import PostgresUrl
from friendly_computing_machine.src.friendly_computing_machine.db.util import init_engine
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
    database_url: PostgresUrl,
    web_public_url: FcmWebPublicUrl,
    oidc_issuer_url: FcmOidcIssuerUrl,
    oidc_client_id: FcmOidcClientId,
    oidc_client_secret: FcmOidcClientSecret,
    web_session_secret: FcmWebSessionSecret,
    skip_migration_check: bool = False,
    port: int = typer.Option(8000, envvar="FCM_WEB_PORT", help="Port to serve on"),
):
    """Run the OIDC identity-link web app."""
    import uvicorn

    from libs.python.alembic import should_run_migration

    # the link flow reads/writes the identity tables, so the app needs the
    # process-wide engine the DAL uses.
    db = create_postgres_context(
        database_url=database_url,
        migrations_package="friendly_computing_machine.src.migrations",
        engine_initializer=init_engine,
    )
    if skip_migration_check:
        logger.info("skipping migration check")
    elif should_run_migration(db.engine, db.alembic_config):
        logger.critical("migration check failed, please migrate")
        raise RuntimeError("need to run migration")
    else:
        logger.info("migration check passed")

    config = WebConfig(
        public_url=web_public_url,
        oidc_issuer_url=oidc_issuer_url,
        oidc_client_id=oidc_client_id,
        oidc_client_secret=oidc_client_secret,
        session_secret=web_session_secret,
    )
    logger.info("starting fcm web identity-link app on port %d", port)
    uvicorn.run(create_app(config), host="0.0.0.0", port=port)
