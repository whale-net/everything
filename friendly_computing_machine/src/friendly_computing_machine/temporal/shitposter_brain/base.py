"""Shared shape of a Shitposter brain job run.

Every brain job (harvest, reflect, write, snapshot) runs as FCM's service
subject with no on_behalf_of. At most one job per persona runs at a time: the
runner takes the per-persona lock by inserting a `running` row, and a job that
finds one already present records a `skipped` row and returns. A job's
writes land in one transaction in its final activity.
"""

from dataclasses import dataclass
from datetime import timedelta
from typing import Any

from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobTrigger,
)

# Workflow execution timeout per job kind; hitting it marks the run failed.
BRAIN_JOB_TIMEOUTS: dict[ShitposterBrainJobKind, timedelta] = {
    ShitposterBrainJobKind.HARVEST: timedelta(minutes=15),
    ShitposterBrainJobKind.REFLECT: timedelta(minutes=30),
    ShitposterBrainJobKind.WRITE: timedelta(minutes=30),
    ShitposterBrainJobKind.SNAPSHOT: timedelta(minutes=5),
}


@dataclass
class BrainJobInput:
    persona_id: int
    job_kind: str
    trigger: str = ShitposterBrainJobTrigger.SCHEDULE.value


@dataclass
class BrainJobResult:
    run_id: int
    status: str
    skip_reason: str | None = None
    details: dict[str, Any] | None = None
