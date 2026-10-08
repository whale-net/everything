"""ShitposterChannelScheduleWorkflow on the in-process Temporal test server.

Real schedule and ShitpostWorkflow over SQLite with fake whagent and Slack.
Each slot's gate read goes through a recording activity that wraps the DAL, so
gap spacing is read from the parent's history timestamps.

Red-proof run: the start signal made a no-op   -> opt-out/fast opt-in test fails.
"""

import asyncio
import datetime
from contextlib import asynccontextmanager

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select
from temporalio import activity
from temporalio.testing import WorkflowEnvironment
from temporalio.worker import Replayer, Worker
from temporalio.worker.workflow_sandbox import (
    SandboxedWorkflowRunner,
    SandboxRestrictions,
)

from friendly_computing_machine.src.friendly_computing_machine.db import dal
from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterChannelOptIn,
    ShitposterKillSwitch,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterScheduledSkip,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
    activity as act,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
    schedule_control as ctl,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
    schedule_workflow as sched,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
    workflow as wf,
)

CHAN = "C_SCHED"
APP_ENV = "test"
PERSONA_TEXT = "You are the unhinged office gremlin."
SERVICE = ("https://kc/realms/x", "fcm-service-sub")
TASK_QUEUE = "fcm-schedule-test"
GATE_ACTIVITY = "shitposter_gate_activity"
MIN_GAP = datetime.timedelta(hours=4)
MAX_GAP = datetime.timedelta(hours=12)
TABLES = [
    SlackChannel.__table__,
    SlackUser.__table__,
    ShitposterChannelOptIn.__table__,
    ShitposterKillSwitch.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterPost.__table__,
    ShitposterContextSnapshot.__table__,
    ShitposterScheduledSkip.__table__,
]


class FakeWhagent:
    def __init__(self):
        self.starts = []
        self.turns = []
        self._sessions = 0
        self._replies = []

    def service_subject(self):
        return SERVICE

    def supports_pinned_context(self):
        return False

    def start_session(self, agent_id, first_turn=None, on_behalf_of=None, pinned_context=None):
        self._sessions += 1
        self.starts.append({"first_turn": first_turn, "on_behalf_of": on_behalf_of})
        return type("S", (), {"session_id": f"sess-{self._sessions}"})()

    def send_turn(self, session_id, input):
        self.turns.append((session_id, input))

    def read_transcript(self, session_id, from_seq=0):
        return [], 5

    def get_session(self, session_id):
        return type("S", (), {"state": act._STATE_DONE})()

    def latest_assistant_message(self, session_id, from_seq=0):
        self._replies.append(1)
        return (f"post {len(self._replies)}", from_seq + 1)


class FakeSlack:
    def __init__(self):
        self.posts = []

    def post(self, channel, text, thread_ts=None):
        self.posts.append((channel, text, thread_ts))
        return f"100.{len(self.posts):06d}"

    def chat_postEphemeral(self, **kw):
        pass


# A test hook: the next gate read computes its verdict, then parks until released.
HOLD = {"armed": False, "entered": None, "release": None}


@activity.defn(name=GATE_ACTIVITY)
async def gate_activity(channel_slack_id: str):
    """Same name as the production gate, so parent and child both resolve here."""
    result = dal.shitposter_gate(channel_slack_id)
    verdict = act.GateActivityResult(allowed=result.allowed, reason=result.reason)
    if HOLD["armed"]:
        HOLD["armed"] = False
        HOLD["entered"].set()
        await HOLD["release"].wait()
    return verdict


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
    with Session(engine) as s:
        persona = ShitposterPersona(name="shitposter")
        s.add(persona)
        s.flush()
        s.add(ShitposterPersonaRevision(persona_id=persona.id, persona_text=PERSONA_TEXT))
        s.commit()
    shitposter_dal.set_channel_opt_in(CHAN, True, "U_ADMIN")
    return engine


@pytest.fixture(autouse=True)
def _env(monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_AGENT_ID", "agent-1")
    monkeypatch.setattr(act, "_POLL_INTERVAL_SECONDS", 0.05)
    HOLD.update(armed=False, entered=None, release=None)


@pytest.fixture
def whagent(monkeypatch):
    fake = FakeWhagent()
    monkeypatch.setattr(act, "get_whagent_client", lambda: fake)
    return fake


@pytest.fixture
def slack(monkeypatch):
    fake = FakeSlack()
    monkeypatch.setattr(act, "slack_post_thread_message", fake.post)
    monkeypatch.setattr(act, "get_slack_web_client", lambda: fake)
    return fake


@asynccontextmanager
async def _worker(env):
    async with Worker(
        env.client,
        task_queue=TASK_QUEUE,
        workflows=[sched.ShitposterChannelScheduleWorkflow, wf.ShitpostWorkflow],
        activities=[
            gate_activity,
            act.resolve_persona_activity,
            act.generate_shitpost_activity,
            act.check_guardrails_activity,
            act.post_and_record_shitpost_activity,
            act.send_ephemeral_notice_activity,
            act.resolve_snapshot_activity,
            act.record_scheduled_skip_activity,
        ],
        workflow_runner=SandboxedWorkflowRunner(
            restrictions=SandboxRestrictions.default.with_passthrough_all_modules()
        ),
    ):
        yield


async def _ensure(env):
    await ctl.ensure_schedule_async(env.client, CHAN, TASK_QUEUE, APP_ENV)
    return env.client.get_workflow_handle(ctl.schedule_workflow_id(CHAN, APP_ENV))


async def _gate_times(handle):
    """Parent gate reads, one per slot: the instants each cadence step checked."""
    times = []
    async for ev in handle.fetch_history_events():
        if not ev.HasField("activity_task_scheduled_event_attributes"):
            continue
        attrs = ev.activity_task_scheduled_event_attributes
        if attrs.activity_type.name == GATE_ACTIVITY:
            times.append(ev.event_time.ToDatetime().replace(tzinfo=datetime.timezone.utc))
    return times


async def _wait_posts(engine, n, timeout=60):
    deadline = asyncio.get_running_loop().time() + timeout
    while True:
        with Session(engine) as s:
            count = len(s.exec(select(ShitposterPost)).all())
        if count >= n:
            return count
        if asyncio.get_running_loop().time() > deadline:
            return count
        await asyncio.sleep(0.05)


def _post_count(engine):
    with Session(engine) as s:
        return len(s.exec(select(ShitposterPost)).all())


def _gaps(times):
    return [b - a for a, b in zip(times, times[1:])]


def _run(coro):
    return asyncio.run(coro)


def test_gaps_are_in_4h_12h_and_vary(engine, slack, whagent):
    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                started_at = await env.get_current_time()
                await env.sleep(datetime.timedelta(hours=60))
                times = await _gate_times(handle)
                posts = await _wait_posts(engine, len(times))
                return started_at, times, posts

    started_at, times, posts = _run(go())
    assert len(times) >= 4
    assert MIN_GAP <= times[0] - started_at <= MAX_GAP
    gaps = _gaps(times)
    assert all(MIN_GAP <= g <= MAX_GAP for g in gaps), gaps
    assert len(set(gaps)) > 1
    assert posts == len(times)


def test_scheduled_posts_send_no_on_behalf_of(engine, slack, whagent):
    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                await env.sleep(datetime.timedelta(hours=30))
                await _wait_posts(engine, 2)
                await handle.signal(sched.ShitposterChannelScheduleWorkflow.stop)

    _run(go())
    assert len(whagent.starts) >= 2
    assert all(s["on_behalf_of"] is None for s in whagent.starts)
    with Session(engine) as s:
        posts = list(s.exec(select(ShitposterPost)).all())
    assert posts and all(
        (p.principal_iss, p.principal_sub, p.principal_kind) == (*SERVICE, "service")
        for p in posts
    )


def test_restart_replay_of_mid_gap_and_post_history_is_deterministic(engine, slack, whagent):
    """A restarted worker replays history; a mid-gap and a post-filled history must replay clean."""

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                await env.sleep(datetime.timedelta(hours=1))  # inside the first gap
                mid_gap = await handle.fetch_history()
                await env.sleep(datetime.timedelta(hours=60))
                times = await _gate_times(handle)
                posts = await _wait_posts(engine, len(times))
                full = await handle.fetch_history()
                return mid_gap, full, times, posts

    mid_gap, full, times, posts = _run(go())
    assert len(times) >= 4
    assert posts == len(times)
    assert all(MIN_GAP <= g <= MAX_GAP for g in _gaps(times))
    replayer = Replayer(
        workflows=[sched.ShitposterChannelScheduleWorkflow],
        workflow_runner=SandboxedWorkflowRunner(
            restrictions=SandboxRestrictions.default.with_passthrough_all_modules()
        ),
    )
    _run(replayer.replay_workflow(mid_gap))
    _run(replayer.replay_workflow(full))


def test_double_start_is_a_noop(engine, slack, whagent):
    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                started_at = await env.get_current_time()
                first_run = (await handle.describe()).run_id
                await _ensure(env)
                await _ensure(env)
                assert (await handle.describe()).run_id == first_run
                await env.sleep(datetime.timedelta(hours=12))
                starts = [
                    ev
                    async for ev in handle.fetch_history_events()
                    if ev.HasField("workflow_execution_started_event_attributes")
                ]
                times = await _gate_times(handle)
                return started_at, starts, times

    started_at, starts, times = _run(go())
    assert len(starts) == 1
    # a reset timer would push the first read past the original 12h window
    assert times and times[0] - started_at <= MAX_GAP


def test_silence_then_resume_draws_fresh_gap_no_burst(engine, slack, whagent):
    shitposter_dal.set_kill_switch(False, "op")

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                await env.sleep(datetime.timedelta(hours=30))
                silenced_posts = _post_count(engine)
                shitposter_dal.set_kill_switch(True, "op")
                resumed_at = await env.get_current_time()
                await handle.signal(sched.ShitposterChannelScheduleWorkflow.resume)
                await env.sleep(datetime.timedelta(hours=30))
                times = await _gate_times(handle)
                posts = await _wait_posts(engine, 1)
                return silenced_posts, resumed_at, times, posts

    silenced_posts, resumed_at, times, posts = _run(go())
    assert silenced_posts == 0
    after = [t for t in times if t > resumed_at]
    assert after, "no gate read after resume"
    # no post inside the 4h floor after re-enable
    assert after[0] - resumed_at >= MIN_GAP
    assert posts >= 1


def test_opt_out_stops_scheduling(engine, slack, whagent):
    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                await env.sleep(datetime.timedelta(hours=1))
                await handle.signal(sched.ShitposterChannelScheduleWorkflow.stop)
                stopped_at = await env.get_current_time()
                await env.sleep(datetime.timedelta(hours=60))
                times = await _gate_times(handle)
                return stopped_at, times

    stopped_at, times = _run(go())
    assert [t for t in times if t > stopped_at] == []
    assert _post_count(engine) == 0


def test_opt_out_then_fast_opt_in_reenables_schedule(engine, slack, whagent):
    """Opt-out followed by opt-in before the run processes the stop must still cadence."""

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                await env.sleep(datetime.timedelta(hours=1))
                await handle.signal(sched.ShitposterChannelScheduleWorkflow.stop)
                # no time advance between stop and re-enable: the race window
                await _ensure(env)
                reenabled_at = await env.get_current_time()
                await env.sleep(datetime.timedelta(hours=60))
                times = await _gate_times(handle)
                posts = await _wait_posts(engine, 1)
                return reenabled_at, times, posts

    reenabled_at, times, posts = _run(go())
    after = [t for t in times if t > reenabled_at]
    assert after, "schedule did not resume after fast opt-in"
    assert after[0] - reenabled_at >= MIN_GAP
    assert posts >= 1


def test_resume_while_gate_read_in_flight_is_not_lost(engine, slack, whagent):
    """A resume landing during the silenced gate read must still restart the gap."""
    shitposter_dal.set_kill_switch(False, "op")

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with _worker(env):
                handle = await _ensure(env)
                HOLD.update(
                    armed=True,
                    entered=asyncio.Event(),
                    release=asyncio.Event(),
                )
                await env.sleep(datetime.timedelta(hours=13))
                await asyncio.wait_for(HOLD["entered"].wait(), timeout=30)
                shitposter_dal.set_kill_switch(True, "op")
                resumed_at = await env.get_current_time()
                await handle.signal(sched.ShitposterChannelScheduleWorkflow.resume)
                HOLD["release"].set()
                await env.sleep(datetime.timedelta(hours=30))
                times = await _gate_times(handle)
                posts = await _wait_posts(engine, 1)
                return resumed_at, times, posts

    resumed_at, times, posts = _run(go())
    after = [t for t in times if t > resumed_at]
    assert after, "resume lost during gate read"
    assert after[0] - resumed_at >= MIN_GAP
    assert posts >= 1
