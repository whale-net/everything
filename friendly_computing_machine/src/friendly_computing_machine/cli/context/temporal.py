import logging
import os
from dataclasses import dataclass
from typing import Annotated

import typer

from friendly_computing_machine.cli.context.app_env import FILENAME as APP_ENV_FILENAME
from friendly_computing_machine.cli.context.app_env import T_app_env, setup_app_env
from friendly_computing_machine.temporal.util import init_temporal

logger = logging.getLogger(__name__)
FILENAME = os.path.basename(__file__)

T_temporal_host = Annotated[str, typer.Option(..., envvar="TEMPORAL_HOST")]


@dataclass
class TemporalConfig:
    host: str
    # TODO - queue names?


def setup_temporal(
    ctx: typer.Context,
    temporal_host: T_temporal_host,
    app_env: T_app_env,
):
    logger.debug("temporal client setup starting")
    setup_app_env(ctx, app_env)
    init_temporal(
        host=temporal_host,
        app_env=ctx.obj[APP_ENV_FILENAME]["app_env"],
    )
    ctx.obj[FILENAME] = TemporalConfig(
        host=temporal_host,
    )
    logger.debug("temporal client setup complete")
