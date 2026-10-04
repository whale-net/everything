"""The weekly music-poll selection activity (krill M5)."""

import datetime
import logging
import random

from temporalio import activity

from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_selection import (
    SelectedOption,
    select_scheduled_poll_options,
)

logger = logging.getLogger(__name__)


@activity.defn
async def select_music_poll_options_activity(
    slack_channel_slack_id: str,
) -> list[SelectedOption] | None:
    """Pick this week's poll options for one music-poll channel.

    The selection, the clock and the tie-break randomness all
    live inside the activity, so the workflow that calls it only
    orchestrates: it posts the options this returns, or -- when
    the activity returns None because fewer than three songs
    were pickable -- leaves the previous poll open and skips
    the week.
    """
    options = select_scheduled_poll_options(
        slack_channel_slack_id,
        datetime.datetime.now(datetime.UTC),
        rng=random.Random(),
    )
    if options is None:
        logger.info(
            "weekly music poll skipped for %s: fewer than 3 pickable songs",
            slack_channel_slack_id,
        )
    else:
        logger.info(
            "weekly music poll picked %s options for %s",
            len(options),
            slack_channel_slack_id,
        )
    return options
