"""Shared shape of a Shitposter brain job run.

Every brain job (harvest, reflect, write, snapshot) runs as FCM's service
subject with no on_behalf_of. At most one job per persona runs at a time: the
runner takes the per-persona lock by inserting a `running` row, and a job that
finds one already present records a `skipped` row and returns. A job's
writes land in one transaction in its final activity.
"""

from collections.abc import Callable
from dataclasses import dataclass
from datetime import timedelta
from typing import Any

from sqlmodel import Session

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

# A running row older than its job timeout plus this slack has no live workflow
# behind it (the worker died before finishing), so the next run takes the lock over.
STALE_LOCK_SLACK = timedelta(minutes=5)


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


@dataclass
class BeginResult:
    run_id: int
    acquired: bool
    skip_reason: str | None = None


@dataclass
class ApplyParams:
    run_id: int
    job_kind: str
    payload: dict[str, Any]


@dataclass
class FailParams:
    run_id: int
    error: str


@dataclass
class ApplyOutcome:
    # succeeded or no_op
    status: str
    details: dict[str, Any] | None = None


@dataclass(frozen=True)
class JobBody:
    """A job's compute step (no writes) and its apply step.

    `apply` receives the run's session and must write only through it and never
    commit: the runner commits the writes, the run status, and the input-consumption
    markers together, or rolls all of them back on an exception.
    """

    compute: Callable[[BrainJobInput], dict[str, Any]]
    apply: Callable[[Session, int, dict[str, Any]], ApplyOutcome]


JOB_BODIES: dict[str, JobBody] = {}


def register_job_body(job_kind: ShitposterBrainJobKind, body: JobBody) -> None:
    JOB_BODIES[job_kind.value] = body


def job_body(job_kind: str) -> JobBody:
    body = JOB_BODIES.get(job_kind)
    if body is None:
        raise RuntimeError(f"no brain job body registered for {job_kind}")
    return body
