import logging
from typing import Optional

import typer

from friendly_computing_machine.src.friendly_computing_machine.cli.context.db import (
    FILENAME as DB_FILENAME,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.db import (
    T_database_url,
    setup_db,
)
from friendly_computing_machine.src.friendly_computing_machine.cli.context.log import (
    setup_logging,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    create_migration,
    run_downgrade,
    run_migration,
)

# Import all models to ensure they are registered with SQLAlchemy
# For example, if you have models in friendly_computing_machine.models.user and friendly_computing_machine.models.post
# import friendly_computing_machine.models.user
# import friendly_computing_machine.models.post
# Add your model imports here:
from friendly_computing_machine.src.friendly_computing_machine.models import (  # noqa: F401
    base,
    genai,
    manman,
    music_poll,
    slack,
    task,
)

logger = logging.getLogger(__name__)

migration_app = typer.Typer(
    context_settings={"obj": {}},
)


@migration_app.callback()
def callback(
    ctx: typer.Context,
    database_url: T_database_url,
    log_otlp: bool = False,
):
    logger.debug("CLI callback starting")
    setup_logging(ctx, log_otlp=log_otlp)
    setup_db(ctx, database_url)
    logger.debug("CLI callback complete")


@migration_app.command("run")
def cli_migration_run(
    ctx: typer.Context,
):
    logger.info("running migration")
    run_migration(ctx.obj[DB_FILENAME].engine, ctx.obj[DB_FILENAME].alembic_config)
    logger.info("migration complete")


@migration_app.command("create")
def cli_migration_create(ctx: typer.Context, message: Optional[str] = None):
    logger.info("creating migration")
    create_migration(
        ctx.obj[DB_FILENAME].engine, ctx.obj[DB_FILENAME].alembic_config, message
    )
    logger.info("migration created")


@migration_app.command("downgrade")
def cli_migration_downgrade(ctx: typer.Context, revision: str):
    logger.info("downgrading migration")
    run_downgrade(
        ctx.obj[DB_FILENAME].engine, ctx.obj[DB_FILENAME].alembic_config, revision
    )
    logger.info("migration downgraded")
