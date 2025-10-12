import logging
import os
from dataclasses import dataclass
from typing import Annotated

import alembic
import typer
from sqlalchemy import Engine
from sqlmodel import create_engine

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
    ctx.obj[FILENAME] = DBContext(
        engine=engine,
        alembic_config=alembic.config.Config("./alembic.ini"),
    )
    logger.debug("db setup complete")
