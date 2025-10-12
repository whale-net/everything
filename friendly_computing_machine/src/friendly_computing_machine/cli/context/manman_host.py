import logging
import os
from enum import Enum
from typing import Annotated

import typer

# Import the new ManManAPI class
from friendly_computing_machine.src.friendly_computing_machine.manman.api import (
    ManManExperienceAPI,
    ManManStatusAPI,
)

logger = logging.getLogger(__name__)
FILENAME = os.path.basename(__file__)


class SupportedAPI(Enum):
    status = "status"
    experience = "experience"


T_manman_host_url = Annotated[str, typer.Option(..., envvar="MANMAN_HOST_URL")]


def setup_manman_status_api(
    ctx: typer.Context,
    url: T_manman_host_url,
):
    """
    Setup the ManMan status API.
    """
    url = url.strip()
    # remove trailing slashes
    url = url.rstrip("/")
    # TODO - figure out why I need to do this
    ManManStatusAPI.init(url + "/status")
    logger.info(f"ManMan Status API initialized with host: {url}")
    ctx.obj.setdefault(FILENAME, {})[SupportedAPI.status] = ManManStatusAPI


def setup_manman_experience_api(
    ctx: typer.Context,
    url: T_manman_host_url,
):
    """
    Setup the ManMan experience API.
    """
    url = url.strip()
    # remove trailing slashes
    url = url.rstrip("/")
    # TODO - figure out why I need to do this
    ManManExperienceAPI.init(url + "/experience")
    logger.info(f"ManMan Experience API initialized with host: {url}")
    ctx.obj.setdefault(FILENAME, {})[SupportedAPI.experience] = ManManExperienceAPI
