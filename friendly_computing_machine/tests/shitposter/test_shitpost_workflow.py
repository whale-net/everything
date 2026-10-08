"""ShitpostWorkflow on the in-process Temporal test server.

Real workflow and activities over SQLite; the whagent client and the Slack
client are fakes. Red-proof (each mutation applied, run, reverted):

  * post activity skips its gate re-check        -> kill-switch-mid-generation test
  * on_behalf_of sent for the service subject    -> service-subject test
  * persona text dropped from the first turn     -> persona-first-turn test
  * guardrails skip member-name check            -> member-name test
  * scheduled regeneration loop bound removed    -> regenerate-then-skip test
"""

import asyncio
import datetime
import uuid

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select
from temporalio.testing import WorkflowEnvironment
from temporalio.worker import Worker
from temporalio.worker.workflow_sandbox import (
    SandboxedWorkflowRunner,
    SandboxRestrictions,
)

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
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
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
    workflow as wf,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
    MAX_SCHEDULED_ATTEMPTS,
    NO_SHITPOST_NOTICE,
    ShitpostOutcome,
    ShitpostParams,
)

CHAN = "C_SHIT"
PERSONA_TEXT = "You are the unhinged office gremlin."
HUMAN = ("https://kc/realms/x", "human-sub")
SERVICE = ("https://kc/realms/x", "fcm-service-sub")
TASK_QUEUE = "fcm-shitposter-test"
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
SNAPSHOT_TEXT = "Snapshot: the gremlin loves cheese."


class FakeWhagent:
    """Only the methods the pipeline uses; pinned context is opt-in via `pinned`."""

    def __init__(self, replies, on_generate=None, hang=False, pinned=False):
        self.replies = list(replies)
        self.on_generate = on_generate
        self.hang = hang
        self.pinned = pinned
        self.starts = []
        self.turns = []
        self._sessions = 0

    def service_subject(self):
        return SERVICE

    def supports_pinned_context(self):
        return self.pinned

    def start_session(self, agent_id, first_turn=None, on_behalf_of=None, pinned_context=None):
        self._sessions += 1
        self.starts.append(
            {
                "agent_id": agent_id,
                "first_turn": first_turn,
                "on_behalf_of": on_behalf_of,
                "pinned_context": pinned_context,
            }
        )
        return type("S", (), {"session_id": f"sess-{self._sessions}"})()

    def send_turn(self, session_id, input):
        self.turns.append((session_id, input))

    def read_transcript(self, session_id, from_seq=0):
        return [], 5

    def get_session(self, session_id):
        if self.hang:
            return type("S", (), {"state": act._STATE_RUNNING})()
        if self.on_generate:
            self.on_generate()
        return type("S", (), {"state": act._STATE_DONE})()

    def latest_assistant_message(self, session_id, from_seq=0):
        return (self.replies.pop(0), from_seq + 1)


class FakeSlack:
    def __init__(self):
        self.posts = []
        self.ephemerals = []

    def post(self, channel, text, thread_ts=None):
        self.posts.append((channel, text, thread_ts))
        return f"100.{len(self.posts):06d}"

    def chat_postEphemeral(self, **kw):
        self.ephemerals.append(kw)


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
        s.add(SlackUser(slack_id="U_BOB", name="Bobby Tables", slack_team_slack_id="T1"))
        s.commit()
    shitposter_dal.set_channel_opt_in(CHAN, True, "U_ADMIN")
    return engine


@pytest.fixture
def slack(monkeypatch):
    fake = FakeSlack()
    monkeypatch.setattr(act, "slack_post_thread_message", fake.post)
    monkeypatch.setattr(act, "get_slack_web_client", lambda: fake)
    return fake


@pytest.fixture(autouse=True)
def _env(monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_AGENT_ID", "agent-1")
    monkeypatch.setattr(act, "_POLL_INTERVAL_SECONDS", 0.05)


def _run(whagent, monkeypatch, params):
    monkeypatch.setattr(act, "get_whagent_client", lambda: whagent)

    async def go():
        async with await WorkflowEnvironment.start_time_skipping() as env:
            async with Worker(
                env.client,
                task_queue=TASK_QUEUE,
                workflows=[wf.ShitpostWorkflow],
                activities=[
                    act.shitposter_gate_activity,
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
                return await asyncio.wait_for(
                    env.client.execute_workflow(
                        wf.ShitpostWorkflow.run,
                        params,
                        id=f"shitpost-{uuid.uuid4().hex}",
                        task_queue=TASK_QUEUE,
                    ),
                    timeout=60,
                )

    return asyncio.run(go())


def _posts(engine):
    with Session(engine) as s:
        return list(s.exec(select(ShitposterPost)).all())


def test_scheduled_posts_as_service_subject_without_on_behalf_of(engine, slack, monkeypatch):
    w = FakeWhagent(["a fine shitpost"])
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.POSTED
    assert w.starts[0]["on_behalf_of"] is None
    assert w.starts[0]["agent_id"] == "agent-1"
    [post] = _posts(engine)
    assert (post.principal_iss, post.principal_sub, post.principal_kind) == (*SERVICE, "service")
    assert post.trigger == "scheduled"
    assert post.persona_revision_id is not None
    assert slack.posts == [(CHAN, "a fine shitpost", None)]


def test_persona_text_is_in_first_turn(engine, slack, monkeypatch):
    w = FakeWhagent(["ok post"])
    _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert w.starts[0]["first_turn"].startswith(PERSONA_TEXT)
    assert not hasattr(w, "pinned_context")


def test_summon_records_human_principal_and_on_behalf_of(engine, slack, monkeypatch):
    w = FakeWhagent(["summoned post"])
    res = _run(
        w,
        monkeypatch,
        ShitpostParams(
            CHAN, "summon", principal_iss=HUMAN[0], principal_sub=HUMAN[1],
            topic="cheese", notice_slack_user_id="U_SUMMONER",
        ),
    )
    assert res.outcome == ShitpostOutcome.POSTED
    assert w.starts[0]["on_behalf_of"] == HUMAN
    assert "cheese" in w.starts[0]["first_turn"]
    [post] = _posts(engine)
    assert (post.principal_iss, post.principal_sub, post.principal_kind) == (*HUMAN, "human")
    assert post.thread_owner_slack_user_id == "U_SUMMONER"


def test_riff_continues_session_in_thread(engine, slack, monkeypatch):
    w = FakeWhagent(["riffing"])
    res = _run(
        w,
        monkeypatch,
        ShitpostParams(
            CHAN, "riff", principal_iss=HUMAN[0], principal_sub=HUMAN[1],
            thread_ts="99.000001", whagent_session_id="sess-old",
            thread_owner_slack_user_id="U_OWNER",
        ),
    )
    assert res.outcome == ShitpostOutcome.POSTED
    assert w.starts == []
    assert w.turns[0][0] == "sess-old"
    assert slack.posts[0][2] == "99.000001"
    [post] = _posts(engine)
    assert post.trigger == "riff" and post.thread_ts == "99.000001"
    assert post.whagent_session_id == "sess-old"


@pytest.mark.parametrize(
    "bad",
    ["hey <@U123ABC> look", "<!channel> alert", "bobby tables did it", "just kys lol"],
)
def test_guardrails_block_mention_name_and_filter(engine, slack, monkeypatch, bad):
    w = FakeWhagent([bad])
    res = _run(
        w, monkeypatch,
        ShitpostParams(CHAN, "summon", principal_iss=HUMAN[0], principal_sub=HUMAN[1],
                       notice_slack_user_id="U_S"),
    )
    assert res.outcome == ShitpostOutcome.BLOCKED_GUARDRAIL
    assert slack.posts == [] and _posts(engine) == []
    assert slack.ephemerals[0]["text"] == NO_SHITPOST_NOTICE
    assert len(w.starts) == 1  # interactive: no retry


def test_scheduled_regenerates_then_skips(engine, slack, monkeypatch):
    w = FakeWhagent(["<@U1> no"] * MAX_SCHEDULED_ATTEMPTS)
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.BLOCKED_GUARDRAIL
    assert len(w.starts) == MAX_SCHEDULED_ATTEMPTS
    assert slack.posts == [] and slack.ephemerals == []


def test_scheduled_recovers_on_regeneration(engine, slack, monkeypatch):
    w = FakeWhagent(["<@U1> no", "clean one"])
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.POSTED
    assert len(w.starts) == 2
    assert slack.posts[0][1] == "clean one"


def test_summon_timeout_sends_notice_and_posts_nothing(engine, slack, monkeypatch):
    # shrink the 60s bound so the real-time poll loop finishes quickly
    monkeypatch.setattr(wf, "INTERACTIVE_DEADLINE", datetime.timedelta(seconds=7))
    w = FakeWhagent([], hang=True)
    res = _run(
        w, monkeypatch,
        ShitpostParams(CHAN, "summon", principal_iss=HUMAN[0], principal_sub=HUMAN[1],
                       notice_slack_user_id="U_S"),
    )
    assert res.outcome == ShitpostOutcome.TIMED_OUT
    assert slack.posts == [] and _posts(engine) == []
    assert slack.ephemerals[0]["text"] == NO_SHITPOST_NOTICE
    assert slack.ephemerals[0]["user"] == "U_S"


def test_failed_generation_posts_nothing(engine, slack, monkeypatch):
    w = FakeWhagent([])
    w.start_session = lambda *a, **k: (_ for _ in ()).throw(RuntimeError("boom"))
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.SKIPPED_WRITER_UNAVAILABLE
    assert slack.posts == []


def test_gate_closed_at_start_skips(engine, slack, monkeypatch):
    shitposter_dal.set_channel_opt_in(CHAN, False, "U_ADMIN")
    w = FakeWhagent(["x"])
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.SKIPPED_GATE
    assert w.starts == [] and slack.posts == []


def test_kill_switch_flipped_mid_generation_posts_nothing(engine, slack, monkeypatch):
    w = FakeWhagent(["would have posted"], on_generate=lambda: shitposter_dal.set_kill_switch(False, "op"))
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.SKIPPED_GATE
    assert res.reason == "silenced"
    assert slack.posts == [] and _posts(engine) == []


def test_opt_out_mid_generation_posts_nothing(engine, slack, monkeypatch):
    w = FakeWhagent(
        ["would have posted"],
        on_generate=lambda: shitposter_dal.set_channel_opt_in(CHAN, False, "U_ADMIN"),
    )
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.SKIPPED_GATE
    assert slack.posts == []


def _add_snapshot(engine, version, text=SNAPSHOT_TEXT):
    with Session(engine) as s:
        persona = s.exec(select(ShitposterPersona)).first()
        snap = ShitposterContextSnapshot(
            persona_id=persona.id, version=version, brain_job_run_id=1,
            token_count=10, rendered_text=text,
        )
        s.add(snap)
        s.commit()
        s.refresh(snap)
        return snap.id


def _skips(engine):
    with Session(engine) as s:
        return list(s.exec(select(ShitposterScheduledSkip)).all())


def test_scheduled_post_records_latest_snapshot_and_uses_it_as_first_turn(engine, slack, monkeypatch):
    _add_snapshot(engine, 1, "old snapshot")
    latest = _add_snapshot(engine, 2)
    w = FakeWhagent(["snapshot post"])
    _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert w.starts[0]["first_turn"].startswith(SNAPSHOT_TEXT)
    assert PERSONA_TEXT not in w.starts[0]["first_turn"]
    assert w.starts[0]["pinned_context"] is None
    [post] = _posts(engine)
    assert post.context_snapshot_id == latest


def test_pinned_context_carries_snapshot_when_supported(engine, slack, monkeypatch):
    snap = _add_snapshot(engine, 1)
    w = FakeWhagent(["pinned post"], pinned=True)
    _run(w, monkeypatch, ShitpostParams(CHAN, "summon", principal_iss=HUMAN[0],
                                        principal_sub=HUMAN[1], notice_slack_user_id="U_S"))
    assert w.starts[0]["pinned_context"] == SNAPSHOT_TEXT
    assert SNAPSHOT_TEXT not in w.starts[0]["first_turn"]
    [post] = _posts(engine)
    assert post.context_snapshot_id == snap


def test_no_snapshot_falls_back_to_persona_first_turn_with_null_id(engine, slack, monkeypatch):
    w = FakeWhagent(["persona post"], pinned=True)
    _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert w.starts[0]["first_turn"].startswith(PERSONA_TEXT)
    assert w.starts[0]["pinned_context"] is None
    [post] = _posts(engine)
    assert post.context_snapshot_id is None


def test_explicit_snapshot_id_overrides_latest(engine, slack, monkeypatch):
    older = _add_snapshot(engine, 1, "draft snapshot")
    _add_snapshot(engine, 2)
    w = FakeWhagent(["draft post"])
    _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled", context_snapshot_id=older))
    assert w.starts[0]["first_turn"].startswith("draft snapshot")
    [post] = _posts(engine)
    assert post.context_snapshot_id == older


def test_riff_on_existing_session_keeps_its_snapshot_id(engine, slack, monkeypatch):
    older = _add_snapshot(engine, 1)
    _add_snapshot(engine, 2)
    w = FakeWhagent(["riffing"])
    _run(w, monkeypatch, ShitpostParams(
        CHAN, "riff", principal_iss=HUMAN[0], principal_sub=HUMAN[1],
        thread_ts="99.000001", whagent_session_id="sess-old",
        thread_owner_slack_user_id="U_OWNER", context_snapshot_id=older,
    ))
    assert w.starts == []
    [post] = _posts(engine)
    assert post.context_snapshot_id == older


def test_scheduled_writer_unavailable_records_skip_and_returns(engine, slack, monkeypatch):
    snap = _add_snapshot(engine, 1)
    w = FakeWhagent([])
    w.start_session = lambda *a, **k: (_ for _ in ()).throw(RuntimeError("whagent down"))
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.SKIPPED_WRITER_UNAVAILABLE
    assert res.reason == "writer_unavailable"
    assert slack.posts == [] and slack.ephemerals == []
    [skip] = _skips(engine)
    assert skip.reason == "writer_unavailable"
    assert skip.context_snapshot_id == snap
    assert skip.slack_channel_id is not None


def test_scheduled_writer_timeout_records_skip(engine, slack, monkeypatch):
    monkeypatch.setattr(wf, "SCHEDULED_DEADLINE", datetime.timedelta(seconds=7))
    monkeypatch.setattr(wf, "MAX_SCHEDULED_ATTEMPTS", 1)
    w = FakeWhagent([], hang=True)
    res = _run(w, monkeypatch, ShitpostParams(CHAN, "scheduled"))
    assert res.outcome == ShitpostOutcome.SKIPPED_WRITER_UNAVAILABLE
    [skip] = _skips(engine)
    assert skip.context_snapshot_id is None


def test_summon_writer_unavailable_sends_notice_and_no_skip(engine, slack, monkeypatch):
    w = FakeWhagent([])
    w.start_session = lambda *a, **k: (_ for _ in ()).throw(RuntimeError("whagent down"))
    res = _run(w, monkeypatch, ShitpostParams(
        CHAN, "summon", principal_iss=HUMAN[0], principal_sub=HUMAN[1],
        notice_slack_user_id="U_S",
    ))
    assert res.outcome == ShitpostOutcome.FAILED_GENERATION
    assert slack.ephemerals[0]["text"] == NO_SHITPOST_NOTICE
    assert _skips(engine) == []


def test_riff_writer_unavailable_sends_notice_and_no_skip(engine, slack, monkeypatch):
    w = FakeWhagent([])
    w.start_session = lambda *a, **k: (_ for _ in ()).throw(RuntimeError("whagent down"))
    res = _run(w, monkeypatch, ShitpostParams(
        CHAN, "riff", principal_iss=HUMAN[0], principal_sub=HUMAN[1],
        thread_ts="99.000001", thread_owner_slack_user_id="U_OWNER",
        notice_slack_user_id="U_S",
    ))
    assert res.outcome == ShitpostOutcome.FAILED_GENERATION
    assert slack.posts == []
    assert slack.ephemerals[0]["text"] == NO_SHITPOST_NOTICE
    assert _skips(engine) == []
