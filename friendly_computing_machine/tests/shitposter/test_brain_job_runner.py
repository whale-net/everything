"""ShitposterBrainJobWorkflow on the in-process Temporal test server (SQLite).

Covers the per-persona lock: an overlapping or operator-triggered job is recorded
as skipped, a body that raises after its writes leaves no partial writes, and a
job past its timeout is recorded failed. A successful reflect run enqueues one
snapshot run; a failed or no-op reflect run enqueues none. Job bodies are fakes registered per test.

Red-proof run: the begin activity always acquiring the lock -> overlap test fails.
"""

import asyncio
import datetime
import threading
from contextlib import asynccontextmanager

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select
from temporalio.client import ScheduleOverlapPolicy, WorkflowFailureError
from temporalio.service import RPCError, RPCStatusCode
from temporalio.testing import WorkflowEnvironment
from temporalio.worker import Worker
from temporalio.worker.workflow_sandbox import (
    SandboxedWorkflowRunner,
    SandboxRestrictions,
)

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobRun,
    ShitposterBrainJobStatus,
    ShitposterBrainJobTrigger,
    ShitposterPersona,
    ShitposterPersonaRevision,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    base,
    control,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.activity import (
    apply_brain_job_activity,
    begin_brain_job_activity,
    compute_brain_job_activity,
    fail_brain_job_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
    BrainJobInput,
    BrainJobResult,
    JobBody,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.workflow import (
    ShitposterBrainJobWorkflow,
)

APP_ENV = "test"
TASK_QUEUE = "fcm-brain-test"
KIND = ShitposterBrainJobKind.SNAPSHOT.value
TABLES = [
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterBrainJobRun.__table__,
]


@pytest.fixture
def engine(monkeypatch):
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    return engine


@pytest.fixture
def persona_id(engine):
    with Session(engine) as s:
        persona = ShitposterPersona(name="shitposter")
        s.add(persona)
        s.flush()
        s.add(ShitposterPersonaRevision(persona_id=persona.id, persona_text="seed"))
        s.commit()
        return persona.id


@pytest.fixture
def gate():
    """Blocks the compute step until the test releases it; the test sees it entered."""
    return {"entered": threading.Event(), "release": threading.Event()}


def _register(monkeypatch, apply=None, compute=None):
    def default_compute(params):
        return {"n": 1}

    def default_apply(session, run_id, payload):
        return ApplyOutcome(status=ShitposterBrainJobStatus.SUCCEEDED.value, details=payload)

    body = JobBody(compute=compute or default_compute, apply=apply or default_apply)
    monkeypatch.setitem(base.JOB_BODIES, KIND, body)


def _blocking_compute(gate):
    def compute(params):
        gate["entered"].set()
        gate["release"].wait(timeout=60)
        return {"n": 1}

    return compute


@asynccontextmanager
async def _worker(env):
    async with Worker(
        env.client,
        task_queue=TASK_QUEUE,
        workflows=[ShitposterBrainJobWorkflow],
        activities=[
            begin_brain_job_activity,
            compute_brain_job_activity,
            apply_brain_job_activity,
            fail_brain_job_activity,
        ],
        workflow_runner=SandboxedWorkflowRunner(
            restrictions=SandboxRestrictions.default.with_passthrough_all_modules()
        ),
    ):
        yield


async def _start_scheduled(env, persona_id):
    return await env.client.start_workflow(
        ShitposterBrainJobWorkflow.run,
        BrainJobInput(
            persona_id=persona_id,
            job_kind=KIND,
            trigger=ShitposterBrainJobTrigger.SCHEDULE.value,
        ),
        id=f"test-scheduled-{persona_id}-{datetime.datetime.now().timestamp()}",
        task_queue=TASK_QUEUE,
    )


async def _wait_entered(gate):
    loop = asyncio.get_running_loop()
    assert await loop.run_in_executor(None, gate["entered"].wait, 30)


def _runs(engine, persona_id):
    with Session(engine) as s:
        return s.exec(
            select(ShitposterBrainJobRun)
            .where(ShitposterBrainJobRun.persona_id == persona_id)
            .order_by(ShitposterBrainJobRun.id)
        ).all()


def _persona_names(engine):
    with Session(engine) as s:
        return [p.name for p in s.exec(select(ShitposterPersona).order_by(ShitposterPersona.id)).all()]


def _run(coro):
    return asyncio.run(coro)


def test_overlapping_operator_trigger_is_recorded_skipped(engine, persona_id, gate, monkeypatch):
    _register(monkeypatch, compute=_blocking_compute(gate))

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                first = await _start_scheduled(env, persona_id)
                await _wait_entered(gate)
                skipped = await control.trigger_brain_job_async(
                    env.client, TASK_QUEUE, persona_id, KIND, app_env=APP_ENV
                )
                gate["release"].set()
                finished = await first.result()
                return skipped, finished

    skipped, finished = _run(go())
    assert skipped.status == ShitposterBrainJobStatus.SKIPPED.value
    assert skipped.skip_reason and "holds the persona lock" in skipped.skip_reason
    assert finished.status == ShitposterBrainJobStatus.SUCCEEDED.value

    runs = _runs(engine, persona_id)
    assert [r.status for r in runs] == [
        ShitposterBrainJobStatus.SUCCEEDED.value,
        ShitposterBrainJobStatus.SKIPPED.value,
    ]
    skip_row = runs[1]
    assert skip_row.trigger == ShitposterBrainJobTrigger.OPERATOR.value
    assert skip_row.skip_reason is not None
    assert skip_row.details == {"holder_run_id": runs[0].id}


class _CapturingClient:
    def __init__(self):
        self.created = []

    async def create_schedule(self, sid, schedule):
        self.created.append((sid, schedule))


def test_schedule_policy_is_skip_on_overlap(persona_id):
    client = _CapturingClient()
    _run(
        control.register_brain_schedule(
            client,
            TASK_QUEUE,
            persona_id,
            KIND,
            every=datetime.timedelta(hours=1),
            app_env=APP_ENV,
        )
    )
    assert len(client.created) == 1
    sid, schedule = client.created[0]
    assert sid == control.schedule_id(persona_id, KIND, APP_ENV)
    assert schedule.policy.overlap == ScheduleOverlapPolicy.SKIP


def test_body_raising_after_write_leaves_no_partial_writes(engine, persona_id, monkeypatch):
    def apply(session, run_id, payload):
        session.add(ShitposterPersona(name="partial-write"))
        session.flush()
        raise RuntimeError("boom after write")

    _register(monkeypatch, apply=apply)

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                with pytest.raises(WorkflowFailureError):
                    await control.trigger_brain_job_async(
                        env.client, TASK_QUEUE, persona_id, KIND, app_env=APP_ENV
                    )

    _run(go())
    assert _persona_names(engine) == ["shitposter"]
    runs = _runs(engine, persona_id)
    assert len(runs) == 1
    assert runs[0].status == ShitposterBrainJobStatus.FAILED.value
    assert "boom after write" in runs[0].error


def test_compute_raising_marks_run_failed(engine, persona_id, monkeypatch):
    def compute(params):
        raise ValueError("compute exploded")

    _register(monkeypatch, compute=compute)

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                with pytest.raises(WorkflowFailureError):
                    await control.trigger_brain_job_async(
                        env.client, TASK_QUEUE, persona_id, KIND, app_env=APP_ENV
                    )

    _run(go())
    runs = _runs(engine, persona_id)
    assert [r.status for r in runs] == [ShitposterBrainJobStatus.FAILED.value]
    assert "compute exploded" in runs[0].error


def test_apply_reporting_failure_keeps_details_and_fails_workflow(engine, persona_id, monkeypatch):
    def apply(session, run_id, payload):
        return ApplyOutcome(
            status=ShitposterBrainJobStatus.FAILED.value,
            details={"dropped": 5},
            error="no valid draft",
        )

    _register(monkeypatch, apply=apply)

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                with pytest.raises(WorkflowFailureError):
                    await control.trigger_brain_job_async(
                        env.client, TASK_QUEUE, persona_id, KIND, app_env=APP_ENV
                    )

    _run(go())
    runs = _runs(engine, persona_id)
    assert [r.status for r in runs] == [ShitposterBrainJobStatus.FAILED.value]
    assert runs[0].details == {"dropped": 5}
    assert runs[0].error == "no valid draft"


def test_timeout_marks_run_failed(engine, persona_id, gate, monkeypatch):
    _register(monkeypatch, compute=_blocking_compute(gate))
    monkeypatch.setitem(base.BRAIN_JOB_TIMEOUTS, ShitposterBrainJobKind.SNAPSHOT, datetime.timedelta(seconds=2))

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _start_scheduled(env, persona_id)
                await _wait_entered(gate)
                try:
                    with pytest.raises(WorkflowFailureError):
                        await handle.result()
                finally:
                    gate["release"].set()

    _run(go())
    runs = _runs(engine, persona_id)
    assert [r.status for r in runs] == [ShitposterBrainJobStatus.FAILED.value]
    assert runs[0].error
    assert _persona_names(engine) == ["shitposter"]


# Reflect-to-snapshot chaining: a successful reflect apply enqueues exactly one snapshot run.

SNAPSHOT_SUFFIX = "-snapshot"


def _register_kind(monkeypatch, job_kind, apply=None, compute=None):
    def default_compute(params):
        return {"n": 1}

    def default_apply(session, run_id, payload):
        return ApplyOutcome(status=ShitposterBrainJobStatus.SUCCEEDED.value, details=payload)

    body = JobBody(compute=compute or default_compute, apply=apply or default_apply)
    monkeypatch.setitem(base.JOB_BODIES, job_kind, body)


async def _start_kind(env, persona_id, job_kind):
    return await env.client.start_workflow(
        ShitposterBrainJobWorkflow.run,
        BrainJobInput(
            persona_id=persona_id,
            job_kind=job_kind,
            trigger=ShitposterBrainJobTrigger.SCHEDULE.value,
        ),
        id=f"test-{job_kind}-{persona_id}-{datetime.datetime.now().timestamp()}",
        task_queue=TASK_QUEUE,
    )


async def _snapshot_started(env, reflect_handle) -> bool:
    try:
        await env.client.get_workflow_handle(f"{reflect_handle.id}{SNAPSHOT_SUFFIX}").describe()
    except RPCError as e:
        if e.status == RPCStatusCode.NOT_FOUND:
            return False
        raise
    return True


def _snapshot_runs(engine, persona_id):
    return [
        r
        for r in _runs(engine, persona_id)
        if r.job_kind == ShitposterBrainJobKind.SNAPSHOT.value
    ]


def test_successful_reflect_enqueues_exactly_one_snapshot(engine, persona_id, monkeypatch):
    snapshot_applies = []

    def snapshot_apply(session, run_id, payload):
        snapshot_applies.append(run_id)
        return ApplyOutcome(status=ShitposterBrainJobStatus.SUCCEEDED.value, details={"v": 1})

    _register_kind(monkeypatch, ShitposterBrainJobKind.REFLECT.value)
    _register_kind(monkeypatch, ShitposterBrainJobKind.SNAPSHOT.value, apply=snapshot_apply)

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _start_kind(env, persona_id, ShitposterBrainJobKind.REFLECT.value)
                reflected = await handle.result()
                snapshot = await env.client.get_workflow_handle(
                    f"{handle.id}{SNAPSHOT_SUFFIX}", result_type=BrainJobResult
                ).result()
                return reflected, snapshot

    reflected, snapshot = _run(go())
    assert reflected.status == ShitposterBrainJobStatus.SUCCEEDED.value
    assert snapshot.status == ShitposterBrainJobStatus.SUCCEEDED.value
    assert len(snapshot_applies) == 1
    snapshot_rows = _snapshot_runs(engine, persona_id)
    assert [r.status for r in snapshot_rows] == [ShitposterBrainJobStatus.SUCCEEDED.value]
    assert snapshot_rows[0].trigger == ShitposterBrainJobTrigger.SCHEDULE.value


def test_failed_reflect_apply_enqueues_no_snapshot(engine, persona_id, monkeypatch):
    def apply(session, run_id, payload):
        session.add(ShitposterPersona(name="partial-write"))
        session.flush()
        raise RuntimeError("apply broke mid-write")

    _register_kind(monkeypatch, ShitposterBrainJobKind.REFLECT.value, apply=apply)
    _register_kind(monkeypatch, ShitposterBrainJobKind.SNAPSHOT.value)

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _start_kind(env, persona_id, ShitposterBrainJobKind.REFLECT.value)
                with pytest.raises(WorkflowFailureError):
                    await handle.result()
                return await _snapshot_started(env, handle)

    assert _run(go()) is False
    assert _persona_names(engine) == ["shitposter"]
    reflect_rows = [r for r in _runs(engine, persona_id) if r.job_kind == ShitposterBrainJobKind.REFLECT.value]
    assert [r.status for r in reflect_rows] == [ShitposterBrainJobStatus.FAILED.value]
    assert _snapshot_runs(engine, persona_id) == []


def test_no_op_reflect_enqueues_no_snapshot(engine, persona_id, monkeypatch):
    def apply(session, run_id, payload):
        return ApplyOutcome(status=ShitposterBrainJobStatus.NO_OP.value, details={"applied": 0})

    _register_kind(monkeypatch, ShitposterBrainJobKind.REFLECT.value, apply=apply)
    _register_kind(monkeypatch, ShitposterBrainJobKind.SNAPSHOT.value)

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _start_kind(env, persona_id, ShitposterBrainJobKind.REFLECT.value)
                reflected = await handle.result()
                return reflected, await _snapshot_started(env, handle)

    reflected, started = _run(go())
    assert reflected.status == ShitposterBrainJobStatus.NO_OP.value
    assert started is False
    assert _snapshot_runs(engine, persona_id) == []
