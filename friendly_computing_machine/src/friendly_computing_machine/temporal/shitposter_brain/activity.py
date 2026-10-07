"""Activities behind ShitposterBrainJobWorkflow: lock, compute, apply, fail.

The `running` row is the lock. Every transition out of `running` goes through
_finish_if_running, so whichever of apply / fail / stale takeover lands first
decides the run and the others become no-ops.
"""

import asyncio
import datetime
import logging
from typing import Any

from sqlalchemy import update
from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, select
from temporalio import activity
from temporalio.exceptions import ApplicationError

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobRun,
    ShitposterBrainJobStatus,
    ShitposterBrainJobTrigger,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    BRAIN_JOB_TIMEOUTS,
    STALE_LOCK_SLACK,
    ApplyParams,
    BeginResult,
    BrainJobInput,
    BrainJobResult,
    FailParams,
    job_body,
)

logger = logging.getLogger(__name__)

_RUNNING = ShitposterBrainJobStatus.RUNNING.value
_FAILED = ShitposterBrainJobStatus.FAILED.value


def _now() -> datetime.datetime:
    return datetime.datetime.now(datetime.timezone.utc)


def _running_row(session: Session, persona_id: int) -> ShitposterBrainJobRun | None:
    return session.exec(
        select(ShitposterBrainJobRun).where(
            ShitposterBrainJobRun.persona_id == persona_id,
            ShitposterBrainJobRun.status == _RUNNING,
        )
    ).first()


def _is_stale(row: ShitposterBrainJobRun, now: datetime.datetime) -> bool:
    started = row.started_at
    if started.tzinfo is None:
        started = started.replace(tzinfo=datetime.timezone.utc)
    limit = BRAIN_JOB_TIMEOUTS[ShitposterBrainJobKind(row.job_kind)] + STALE_LOCK_SLACK
    return now - started > limit


def _finish_if_running(
    session: Session,
    run_id: int,
    status: str,
    *,
    error: str | None = None,
    details: dict[str, Any] | None = None,
) -> bool:
    """Move a run out of `running`; False if it already left it. Caller commits."""
    result = session.execute(
        update(ShitposterBrainJobRun)
        .where(
            ShitposterBrainJobRun.id == run_id,
            ShitposterBrainJobRun.status == _RUNNING,
        )
        .values(status=status, finished_at=_now(), error=error, details=details)
        .execution_options(synchronize_session=False)
    )
    return result.rowcount == 1


def _begin(params: BrainJobInput) -> BeginResult:
    kind = ShitposterBrainJobKind(params.job_kind)
    trigger = ShitposterBrainJobTrigger(params.trigger)
    with SessionManager() as session:
        stale = _running_row(session, params.persona_id)
        if stale is not None and _is_stale(stale, _now()):
            _finish_if_running(
                session,
                stale.id,
                _FAILED,
                error="lock expired: no live job behind the running row",
            )
        run = ShitposterBrainJobRun(
            persona_id=params.persona_id,
            job_kind=kind.value,
            trigger=trigger.value,
            status=_RUNNING,
            started_at=_now(),
        )
        session.add(run)
        try:
            session.commit()
        except IntegrityError:
            session.rollback()
            holder = _running_row(session, params.persona_id)
            if holder is None:
                raise
            reason = f"{kind.value} skipped: run {holder.id} holds the persona lock"
            skipped = ShitposterBrainJobRun(
                persona_id=params.persona_id,
                job_kind=kind.value,
                trigger=trigger.value,
                status=ShitposterBrainJobStatus.SKIPPED.value,
                finished_at=_now(),
                skip_reason=reason,
                details={"holder_run_id": holder.id},
            )
            session.add(skipped)
            session.commit()
            logger.info("brain job skipped: %s persona=%s", reason, params.persona_id)
            return BeginResult(run_id=skipped.id, acquired=False, skip_reason=reason)
        logger.info(
            "brain job started: kind=%s persona=%s run=%s",
            kind.value,
            params.persona_id,
            run.id,
        )
        return BeginResult(run_id=run.id, acquired=True)


def _apply(params: ApplyParams) -> BrainJobResult:
    body = job_body(params.job_kind)
    with SessionManager() as session:
        run = session.exec(
            select(ShitposterBrainJobRun)
            .where(
                ShitposterBrainJobRun.id == params.run_id,
                ShitposterBrainJobRun.status == _RUNNING,
            )
            .with_for_update()
        ).first()
        if run is None:
            # the run was already failed (timeout or takeover); its writes must not land
            raise ApplicationError(
                f"run {params.run_id} is no longer running; discarding apply",
                non_retryable=True,
            )
        outcome = body.apply(session, params.run_id, params.payload)
        run.status = outcome.status
        run.finished_at = _now()
        run.details = outcome.details
        session.commit()
        logger.info(
            "brain job %s: kind=%s run=%s",
            outcome.status,
            params.job_kind,
            params.run_id,
        )
        return BrainJobResult(
            run_id=params.run_id, status=outcome.status, details=outcome.details
        )


def _fail(params: FailParams) -> BrainJobResult:
    with SessionManager() as session:
        if _finish_if_running(session, params.run_id, _FAILED, error=params.error):
            session.commit()
            logger.info("brain job failed: run=%s error=%s", params.run_id, params.error)
        else:
            session.rollback()
        run = session.get(ShitposterBrainJobRun, params.run_id)
        return BrainJobResult(run_id=params.run_id, status=run.status, details=run.details)


@activity.defn
async def begin_brain_job_activity(params: BrainJobInput) -> BeginResult:
    return await asyncio.to_thread(_begin, params)


@activity.defn
async def compute_brain_job_activity(params: BrainJobInput) -> dict[str, Any]:
    return await asyncio.to_thread(job_body(params.job_kind).compute, params)


@activity.defn
async def apply_brain_job_activity(params: ApplyParams) -> BrainJobResult:
    return await asyncio.to_thread(_apply, params)


@activity.defn
async def fail_brain_job_activity(params: FailParams) -> BrainJobResult:
    return await asyncio.to_thread(_fail, params)
