"""Worker-startup registration of the per-persona brain job schedules.

Harvest, reflect, write, snapshot and idea_generate each get one schedule per
persona. The idea_generate registrar removes the schedule instead when its
cadence is "off"; on-demand runs (brain-trigger) work either way.
"""

import logging
from collections.abc import Awaitable, Callable

from temporalio.client import Client

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.harvest import (
    register_harvest_schedule,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.idea_generate import (
    register_idea_generate_schedule,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.reflect import (
    register_reflect_schedule,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.snapshot import (
    register_snapshot_schedule,
    register_idea_generate_schedule,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.write import (
    register_write_schedule,
)

logger = logging.getLogger(__name__)

Register = Callable[..., Awaitable[None]]

# A successful reflect run also enqueues a snapshot; the scheduled one covers quiet periods.
BRAIN_SCHEDULE_REGISTRARS: tuple[Register, ...] = (
    register_harvest_schedule,
    register_reflect_schedule,
    register_write_schedule,
    register_snapshot_schedule,
    register_idea_generate_schedule,
)


async def register_brain_schedules_async(
    client: Client,
    task_queue: str,
    app_env: str | None = None,
) -> None:
    """Run every brain schedule registrar for every persona; a failure is logged and skipped."""
    persona_ids = shitposter_dal.list_persona_ids()
    for persona_id in persona_ids:
        for register in BRAIN_SCHEDULE_REGISTRARS:
            try:
                await register(client, task_queue, persona_id, app_env=app_env)
            except Exception:
                logger.exception(
                    "brain schedule registration failed persona=%s registrar=%s",
                    persona_id,
                    register.__name__,
                )
    logger.info("brain schedules registered personas=%d", len(persona_ids))
