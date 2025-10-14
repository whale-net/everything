import logging
import os
from dataclasses import dataclass
from typing import Annotated

import alembic
import typer
from sqlalchemy import Engine
from sqlmodel import create_engine

import friendly_computing_machine.src.migrations
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
    logger.debug("db setup starting")
    # init_engine(database_url)
    engine = create_engine(
        url=database_url, echo=echo, pool_pre_ping=True, pool_recycle=60
    )
    init_engine(engine=engine)
    
    # Configure Alembic programmatically without requiring alembic.ini
    # This is necessary for containerized environments where the ini file may not exist
    # Pass file_=None to indicate we're configuring programmatically
    alembic_cfg = alembic.config.Config(file_=None, ini_section="alembic")
    
    # Find the migrations directory using Python's module system
    # The migrations are packaged as friendly_computing_machine.src.migrations
    migrations_dir = os.path.dirname(friendly_computing_machine.src.migrations.__file__)
    
    # Set the script location - this is required by Alembic
    alembic_cfg.set_main_option("script_location", migrations_dir)
    
    # Set the file template for migration filenames
    # Format: YYYY_MM_DD_HHMM-{revision_id}_{slug}
    alembic_cfg.set_main_option("file_template", "%%(year)d_%%(month).2d_%%(day).2d_%%(hour).2d%%(minute).2d-%%(rev)s_%%(slug)s")
    
    # Set the database URL from environment (used by migrations in offline mode)
    # In online mode, env.py gets the URL from environment directly
    alembic_cfg.set_main_option("sqlalchemy.url", database_url)
    
    ctx.obj[FILENAME] = DBContext(
        engine=engine,
        alembic_config=alembic_cfg,
    )
    logger.debug("db setup complete")
