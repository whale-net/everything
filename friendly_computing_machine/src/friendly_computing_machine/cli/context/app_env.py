import logging
import os
from typing import Annotated

import typer

logger = logging.getLogger(__name__)
FILENAME = os.path.basename(__file__)

T_app_env = Annotated[str, typer.Option(..., envvar="APP_ENV")]


def setup_app_env(
    ctx: typer.Context,
    app_env: T_app_env,
):
    if ctx.obj.get(FILENAME) is not None:
        logger.debug("app_env already setup")
        return
    logger.debug("app_env setup starting")
    ctx.obj[FILENAME] = {"app_env": app_env}
    logger.debug("app_env setup complete")
