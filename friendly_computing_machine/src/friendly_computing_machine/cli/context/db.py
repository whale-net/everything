import logging
import os
from dataclasses import dataclass
from typing import Annotated

import alembic
import typer
from sqlalchemy import Engine
from sqlmodel import create_engine

try:
    from importlib.resources import files
except ImportError:
    # Python < 3.9 fallback
    from importlib_resources import files

from libs.python.alembic import create_alembic_config
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    init_engine,
)

logger = logging.getLogger(__name__)
FILENAME = os.path.basename(__file__)
T_database_url = Annotated[str, typer.Option(..., envvar="DATABASE_URL")]


@dataclass
class DBContext:
    engine: Engine
    alembic_config: alembic.config.Config


def setup_db(
    ctx: typer.Context,
    database_url: T_database_url,
    echo: bool = False,
):
    """Setup database connection and alembic configuration.
    
    Uses consolidated alembic library for configuration.
    """
    logger.debug("db setup starting")
    
    # Create and initialize engine
    engine = create_engine(
        url=database_url, echo=echo, pool_pre_ping=True, pool_recycle=60
    )
    init_engine(engine=engine)
    
    # Get migrations directory
    migrations_dir = str(files("friendly_computing_machine.src.migrations"))
    
    # Create alembic config using consolidated library
    alembic_cfg = create_alembic_config(
        migrations_dir=migrations_dir,
        database_url=database_url,
        version_table_schema="public",
    )
    
    ctx.obj[FILENAME] = DBContext(
        engine=engine,
        alembic_config=alembic_cfg,
    )
    logger.debug("db setup complete")
