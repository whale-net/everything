"""Client-side entry points for brain jobs: operator trigger and per-kind schedules.

Both start ShitposterBrainJobWorkflow, so both go through the same per-persona
lock as any other run.
"""

import uuid
from datetime import timedelta

from temporalio.client import (
    Client,
    Schedule,
    ScheduleActionStartWorkflow,
    ScheduleIntervalSpec,
    SchedulePolicy,
    ScheduleOverlapPolicy,
    ScheduleSpec,
)
from temporalio.service import RPCError, RPCStatusCode

from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobTrigger,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    BrainJobInput,
    BrainJobResult,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.workflow import (
    ShitposterBrainJobWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    get_app_env,
)


def schedule_id(persona_id: int, job_kind: str, app_env: str | None = None) -> str:
    return f"fcm-{app_env or get_app_env()}-shitposter-brain-{job_kind}-{persona_id}"


async def trigger_brain_job_async(
    client: Client,
    task_queue: str,
    persona_id: int,
    job_kind: str,
    app_env: str | None = None,
) -> BrainJobResult:
    """Run one job now and return its outcome; a held lock returns a skipped result."""
    job_kind = ShitposterBrainJobKind(job_kind).value
    handle = await client.start_workflow(
        ShitposterBrainJobWorkflow.run,
        BrainJobInput(
            persona_id=persona_id,
            job_kind=job_kind,
            trigger=ShitposterBrainJobTrigger.OPERATOR.value,
        ),
        id=f"{schedule_id(persona_id, job_kind, app_env)}-operator-{uuid.uuid4().hex}",
        task_queue=task_queue,
    )
    return await handle.result()


async def register_brain_schedule(
    client: Client,
    task_queue: str,
    persona_id: int,
    job_kind: str,
    every: timedelta,
    app_env: str | None = None,
    overlap: ScheduleOverlapPolicy = ScheduleOverlapPolicy.SKIP,
) -> None:
    """Create the recurring schedule for one persona and job kind; no-op if it exists."""
    job_kind = ShitposterBrainJobKind(job_kind).value
    sid = schedule_id(persona_id, job_kind, app_env)
    schedule = Schedule(
        action=ScheduleActionStartWorkflow(
            ShitposterBrainJobWorkflow.run,
            BrainJobInput(
                persona_id=persona_id,
                job_kind=job_kind,
                trigger=ShitposterBrainJobTrigger.SCHEDULE.value,
            ),
            id=sid,
            task_queue=task_queue,
        ),
        spec=ScheduleSpec(intervals=[ScheduleIntervalSpec(every=every)]),
        policy=SchedulePolicy(overlap=overlap),
    )
    try:
        await client.create_schedule(sid, schedule)
    except RPCError as e:
        if e.status != RPCStatusCode.ALREADY_EXISTS:
            raise
