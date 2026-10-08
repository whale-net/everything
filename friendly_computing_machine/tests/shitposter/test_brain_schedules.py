"""Worker-startup brain schedules: registration per persona, idempotent restart, scheduled reflect enqueues snapshot.

The Temporal time-skipping test server does not implement CreateSchedule, so
registration runs against an in-memory client that reproduces the server's
ALREADY_EXISTS response. The scheduled reflect run starts from the action
payload of the registered schedule, on the real ShitposterBrainJobWorkflow.

Red-proof run: register_brain_schedules_async registering nothing -> fresh namespace test fails.
"""

import asyncio
import datetime
import logging
import uuid
from contextlib import asynccontextmanager

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select
from temporalio.client import ScheduleActionStartWorkflow
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
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.harvest import (
    HARVEST_SCHEDULE_EVERY,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.reflect import (
    REFLECT_SCHEDULE_EVERY,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.schedules import (
    register_brain_schedules_async,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.workflow import (
    ShitposterBrainJobWorkflow,
)

APP_ENV = "test"
TASK_QUEUE = "fcm-brain-schedules-test"
KINDS = ("harvest", "reflect", "write")
TABLES = [
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterBrainJobRun.__table__,
]


class _FakeScheduleClient:
    """In-memory CreateSchedule with the server's ALREADY_EXISTS response."""

    def __init__(self):
        self.schedules = {}
        self.create_attempts = 0

    async def create_schedule(self, sid, schedule):
        self.create_attempts += 1
        if sid in self.schedules:
            raise RPCError("schedule already exists", RPCStatusCode.ALREADY_EXISTS, b"")
        self.schedules[sid] = schedule


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
def personas(engine):
    ids = []
    with Session(engine) as s:
        for name in ("shitposter", "gremlin"):
            persona = ShitposterPersona(name=name)
            s.add(persona)
            s.flush()
            s.add(ShitposterPersonaRevision(persona_id=persona.id, persona_text="seed"))
            ids.append(persona.id)
        s.commit()
    return ids


@pytest.fixture
def fake_bodies(monkeypatch):
    """Fake reflect and snapshot bodies; returns the list snapshot applies append to."""
    snapshot_applies = []

    def default_compute(params):
        return {"n": 1}

    def default_apply(session, run_id, payload):
        return ApplyOutcome(status=ShitposterBrainJobStatus.SUCCEEDED.value, details=payload)

    def snapshot_apply(session, run_id, payload):
        snapshot_applies.append(run_id)
        return ApplyOutcome(status=ShitposterBrainJobStatus.SUCCEEDED.value, details={"v": 1})

    monkeypatch.setitem(
        base.JOB_BODIES,
        ShitposterBrainJobKind.REFLECT.value,
        base.JobBody(compute=default_compute, apply=default_apply),
    )
    monkeypatch.setitem(
        base.JOB_BODIES,
        ShitposterBrainJobKind.SNAPSHOT.value,
        base.JobBody(compute=default_compute, apply=snapshot_apply),
    )
    return snapshot_applies


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


def _runs(engine, persona_id):
    with Session(engine) as s:
        return s.exec(
            select(ShitposterBrainJobRun)
            .where(ShitposterBrainJobRun.persona_id == persona_id)
            .order_by(ShitposterBrainJobRun.id)
        ).all()


async def _wait_for_snapshot_rows(engine, persona_id, timeout_s=30):
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout_s
    while loop.time() < deadline:
        rows = [
            r
            for r in _runs(engine, persona_id)
            if r.job_kind == ShitposterBrainJobKind.SNAPSHOT.value
            and r.status == ShitposterBrainJobStatus.SUCCEEDED.value
        ]
        if rows:
            return rows
        await asyncio.sleep(0.2)
    return []


def _run(coro):
    return asyncio.run(coro)


def _expected_ids(personas):
    return {
        control.schedule_id(pid, kind, APP_ENV) for pid in personas for kind in KINDS
    }


def test_fresh_namespace_creates_harvest_reflect_and_write_per_persona(
    personas, monkeypatch
):
    monkeypatch.setenv("FCM_SHITPOSTER_WRITE_CADENCE_HOURS", "3")
    client = _FakeScheduleClient()

    _run(register_brain_schedules_async(client, TASK_QUEUE, APP_ENV))

    assert set(client.schedules) == _expected_ids(personas)
    for pid in personas:
        every = {
            kind: client.schedules[control.schedule_id(pid, kind, APP_ENV)].spec.intervals[0].every
            for kind in KINDS
        }
        assert every == {
            "harvest": HARVEST_SCHEDULE_EVERY,
            "reflect": REFLECT_SCHEDULE_EVERY,
            "write": datetime.timedelta(hours=3),
        }


def test_second_start_creates_no_duplicate_schedules(personas, monkeypatch, caplog):
    monkeypatch.setenv("FCM_SHITPOSTER_WRITE_CADENCE_HOURS", "3")
    client = _FakeScheduleClient()

    _run(register_brain_schedules_async(client, TASK_QUEUE, APP_ENV))
    first = set(client.schedules)
    with caplog.at_level(logging.ERROR):
        _run(register_brain_schedules_async(client, TASK_QUEUE, APP_ENV))

    assert len(first) == 2 * len(KINDS)
    assert set(client.schedules) == first
    # every second-start attempt hit ALREADY_EXISTS, which is a no-op rather than a failure
    assert client.create_attempts == 2 * len(first)
    assert not [r for r in caplog.records if "registration failed" in r.getMessage()]


def test_scheduled_reflect_run_triggers_snapshot(engine, personas, fake_bodies, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_WRITE_CADENCE_HOURS", "3")
    client = _FakeScheduleClient()
    pid = personas[0]
    _run(register_brain_schedules_async(client, TASK_QUEUE, APP_ENV))
    action = client.schedules[
        control.schedule_id(pid, ShitposterBrainJobKind.REFLECT.value, APP_ENV)
    ].action
    assert isinstance(action, ScheduleActionStartWorkflow)
    scheduled_input: BrainJobInput = action.args[0]
    assert scheduled_input.trigger == ShitposterBrainJobTrigger.SCHEDULE.value

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                await env.client.start_workflow(
                    ShitposterBrainJobWorkflow.run,
                    scheduled_input,
                    id=f"{action.id}-test-{uuid.uuid4().hex}",
                    task_queue=action.task_queue,
                )
                return await _wait_for_snapshot_rows(engine, pid)

    snapshot_rows = _run(go())
    assert [r.status for r in snapshot_rows] == [ShitposterBrainJobStatus.SUCCEEDED.value]
    assert snapshot_rows[0].trigger == ShitposterBrainJobTrigger.SCHEDULE.value
    assert len(fake_bodies) == 1
    reflect_rows = [
        r for r in _runs(engine, pid) if r.job_kind == ShitposterBrainJobKind.REFLECT.value
    ]
    assert [r.status for r in reflect_rows] == [ShitposterBrainJobStatus.SUCCEEDED.value]
    assert reflect_rows[0].trigger == ShitposterBrainJobTrigger.SCHEDULE.value
