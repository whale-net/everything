"""Writer brain job and the draft-first scheduled post path.

Write job: reply validation (malformed and repeated-rank items dropped and
counted, batch cap, zero valid -> failed run with details kept). Scheduled post:
best-ranked unused unexpired draft wins; retired-item and guardrail failures are
discarded and the next draft tried; no usable draft falls back to on-the-spot
generation; a posted draft is used at most once, and its snapshot id is recorded
on the post. Runs on the in-process Temporal test server over SQLite with fake
whagent and Slack clients.

Red-proof (each mutation applied, run, reverted):
  * _pick_draft returns the worst rank               -> best-rank test
  * expiry check removed                             -> expired-draft test
  * retired-item check removed                       -> retired-item test
  * guardrail check removed from draft path          -> guardrail test
  * used_at not set in the post transaction          -> used-at-most-once test
  * validate_drafts keeps repeated ranks             -> duplicate-rank test
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
from friendly_computing_machine.src.friendly_computing_machine.db.dal import shitposter_dal
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobRun,
    ShitposterChannelOptIn,
    ShitposterKillSwitch,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterScheduledSkip,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_draft import (
    ShitposterDraft,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterAttributeStatusEnum,
    ShitposterLoreEntry,
    ShitposterPersonaAttribute,
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
    ShitpostOutcome,
    ShitpostParams,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    write,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobStatus,
)

CHAN = "C_DRAFT"
PERSONA_TEXT = "You are the unhinged office gremlin."
SERVICE = ("https://kc/realms/x", "fcm-service-sub")
TASK_QUEUE = "fcm-shitposter-writer-test"
WRITER_SESSION = "writer-sess-1"
TABLES = [
    SlackChannel.__table__,
    SlackUser.__table__,
    ShitposterChannelOptIn.__table__,
    ShitposterKillSwitch.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterPost.__table__,
    ShitposterBrainJobRun.__table__,
    ShitposterContextSnapshot.__table__,
    ShitposterSnapshotItem.__table__,
    ShitposterPersonaAttribute.__table__,
    ShitposterLoreEntry.__table__,
    ShitposterScheduledSkip.__table__,
    ShitposterDraft.__table__,
]
NOW = datetime.datetime.now(datetime.timezone.utc)


class FakeWhagent:
    ui_public_url = ""

    def __init__(self, replies=()):
        self.replies = list(replies)
        self.starts = []
        self._sessions = 0

    def service_subject(self):
        return SERVICE

    def supports_pinned_context(self):
        return False

    def start_session(self, agent_id, first_turn=None, on_behalf_of=None, pinned_context=None):
        self._sessions += 1
        self.starts.append(first_turn)
        return type("S", (), {"session_id": f"gen-sess-{self._sessions}"})()

    def send_turn(self, session_id, input):
        pass

    def read_transcript(self, session_id, from_seq=0):
        return [], 0

    def get_session(self, session_id):
        return type("S", (), {"state": act._STATE_DONE})()

    def latest_assistant_message(self, session_id, from_seq=0):
        return (self.replies.pop(0), 1)


class FakeSlack:
    def __init__(self):
        self.blocks = []
        self.posts = []

    def post(self, channel, text, thread_ts=None, blocks=None, unfurl=None):
        self.blocks.append(blocks)
        self.posts.append(text)
        return f"200.{len(self.posts):06d}"

    def chat_postEphemeral(self, **kw):
        pass


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
    monkeypatch.setenv("FCM_SHITPOSTER_WRITER_AGENT_ID", "writer-agent")
    monkeypatch.setattr(act, "_POLL_INTERVAL_SECONDS", 0.05)


def _seed(engine, *, drafts=(), attrs=(), lore=(), snapshot_items=()):
    """Persona, writer run, snapshot, drafts (dicts), and optional snapshot items.

    `snapshot_items` is a list of (item_kind, item_id) for the snapshot. Returns
    (persona_id, snapshot_id, run_id).
    """
    with Session(engine) as s:
        persona = s.exec(select(ShitposterPersona)).one()
        run = ShitposterBrainJobRun(
            persona_id=persona.id,
            job_kind="write",
            trigger="schedule",
            status=ShitposterBrainJobStatus.SUCCEEDED.value,
            started_at=NOW,
            details={"whagent_session_id": WRITER_SESSION},
        )
        s.add(run)
        s.flush()
        snap = ShitposterContextSnapshot(
            persona_id=persona.id,
            version=1,
            created_at=NOW,
            brain_job_run_id=run.id,
            token_count=10,
            rendered_text="- the gremlin loves cheese",
        )
        s.add(snap)
        s.flush()
        for kind, item_id in snapshot_items:
            s.add(
                ShitposterSnapshotItem(
                    snapshot_id=snap.id, item_kind=kind, item_id=item_id, rank=1
                )
            )
        for spec in drafts:
            s.add(
                ShitposterDraft(
                    persona_id=persona.id,
                    snapshot_id=snap.id,
                    brain_job_run_id=run.id,
                    text=spec["text"],
                    rank=spec["rank"],
                    created_at=spec.get("created_at", NOW),
                )
            )
        s.commit()
        return persona.id, snap.id, run.id


def _run_scheduled(whagent, monkeypatch, trigger="scheduled"):
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
                    act.post_queued_draft_activity,
                ],
                workflow_runner=SandboxedWorkflowRunner(
                    restrictions=SandboxRestrictions.default.with_passthrough_all_modules()
                ),
            ):
                return await asyncio.wait_for(
                    env.client.execute_workflow(
                        wf.ShitpostWorkflow.run,
                        ShitpostParams(channel_slack_id=CHAN, trigger=trigger),
                        id=f"shitpost-{uuid.uuid4().hex}",
                        task_queue=TASK_QUEUE,
                    ),
                    timeout=60,
                )

    return asyncio.run(go())


def _drafts(engine):
    with Session(engine) as s:
        return list(s.exec(select(ShitposterDraft).order_by(ShitposterDraft.id)).all())


def _posts(engine):
    with Session(engine) as s:
        return list(s.exec(select(ShitposterPost).order_by(ShitposterPost.id)).all())


# ---- write job: reply validation ----


def test_validate_drops_malformed_and_repeated_ranks_and_counts_them():
    items = [
        {"text": "ok", "rank": 1},
        {"text": "", "rank": 2},
        {"text": "string rank", "rank": "1"},
        {"text": "repeated rank", "rank": 1},
        {"text": "bool rank", "rank": True},
        {"text": "zero rank", "rank": 0},
        {"text": "fine", "rank": 3},
        "not an object",
        {"text": "x" * 281, "rank": 4},
    ]
    drafts, dropped = write.validate_drafts(items, batch=5)
    assert [(d["text"], d["rank"]) for d in drafts] == [("ok", 1), ("fine", 3)]
    assert dropped == 7


def test_validate_caps_batch_and_counts_overflow_as_dropped():
    items = [{"text": f"p{r}", "rank": r} for r in (3, 1, 2)]
    drafts, dropped = write.validate_drafts(items, batch=2)
    assert [d["rank"] for d in drafts] == [1, 2]
    assert dropped == 1


def test_parse_array_rejects_reply_without_array():
    assert write._parse_array("here you go: no array") is None
    assert write._parse_array('prose [1, 2') is None
    assert write._parse_array('```json\n[{"text": "a", "rank": 1}]\n```') == [
        {"text": "a", "rank": 1}
    ]


# ---- write job: compute and apply ----


def _write_run(engine, monkeypatch, reply, batch=None):
    """Seed a snapshot, call the write body's compute and apply, return the run details."""
    persona_id, snapshot_id, _ = _seed(engine)
    monkeypatch.setattr(write, "_call_writer", lambda text, n: (WRITER_SESSION, reply))
    if batch is not None:
        monkeypatch.setenv("FCM_SHITPOSTER_DRAFT_BATCH_SIZE", str(batch))
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
        BrainJobInput,
    )

    payload = write._compute(BrainJobInput(persona_id=persona_id, job_kind="write"))
    with Session(engine) as s:
        run = ShitposterBrainJobRun(
            persona_id=persona_id,
            job_kind="write",
            trigger="schedule",
            status="running",
            started_at=NOW,
        )
        s.add(run)
        s.flush()
        outcome = write._apply(s, run.id, payload)
        s.commit()
        run_id = run.id
    return outcome, run_id


def test_write_stores_valid_drafts_and_records_dropped_count(engine, monkeypatch):
    reply = (
        '[{"text": "first", "rank": 1}, {"text": "bad", "rank": "2"}, '
        '{"text": "third", "rank": 3}, {"text": "dup", "rank": 1}]'
    )
    outcome, run_id = _write_run(engine, monkeypatch, reply)
    assert outcome.status == ShitposterBrainJobStatus.SUCCEEDED.value
    assert outcome.details["drafted"] == 2
    assert outcome.details["dropped"] == 2
    assert outcome.details["whagent_session_id"] == WRITER_SESSION
    drafts = _drafts(engine)
    assert [(d.text, d.rank, d.brain_job_run_id) for d in drafts] == [
        ("first", 1, run_id),
        ("third", 3, run_id),
    ]
    assert all(d.used_at is None and d.discarded_at is None for d in drafts)


def test_empty_batch_is_a_failed_run_with_nothing_queued(engine, monkeypatch):
    outcome, _ = _write_run(engine, monkeypatch, "[]")
    assert outcome.status == ShitposterBrainJobStatus.FAILED.value
    assert outcome.error == "writer batch had no valid draft"
    assert outcome.details["dropped"] == 0
    assert _drafts(engine) == []


def test_all_items_invalid_is_a_failed_run_with_dropped_count(engine, monkeypatch):
    outcome, _ = _write_run(engine, monkeypatch, '[{"text": "", "rank": 1}, "junk"]')
    assert outcome.status == ShitposterBrainJobStatus.FAILED.value
    assert outcome.details["dropped"] == 2
    assert _drafts(engine) == []


def test_unparseable_reply_is_a_failed_run(engine, monkeypatch):
    outcome, _ = _write_run(engine, monkeypatch, "I cannot help with that.")
    assert outcome.status == ShitposterBrainJobStatus.FAILED.value
    assert outcome.error == "writer reply has no JSON array"
    assert _drafts(engine) == []


def test_batch_size_env_caps_stored_drafts(engine, monkeypatch):
    reply = '[{"text": "a", "rank": 1}, {"text": "b", "rank": 2}, {"text": "c", "rank": 3}]'
    outcome, _ = _write_run(engine, monkeypatch, reply, batch=2)
    assert outcome.details["drafted"] == 2
    assert outcome.details["dropped"] == 1
    assert [d.text for d in _drafts(engine)] == ["a", "b"]


def test_write_with_no_snapshot_raises_for_the_runner(engine, monkeypatch):
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
        BrainJobInput,
    )

    with Session(engine) as s:
        s.add(ShitposterPersona(name="other"))
        s.commit()
        persona_id = s.exec(select(ShitposterPersona).where(ShitposterPersona.name == "other")).one().id
    with pytest.raises(RuntimeError, match="no context snapshot"):
        write._compute(BrainJobInput(persona_id=persona_id, job_kind="write"))


def test_writer_schedule_cadence_defaults_and_env_override(monkeypatch):
    from friendly_computing_machine.src.friendly_computing_machine import shitposter_config as cfg

    monkeypatch.delenv("FCM_SHITPOSTER_WRITE_CADENCE_HOURS", raising=False)
    assert cfg.write_cadence_hours() == 3
    monkeypatch.setenv("FCM_SHITPOSTER_WRITE_CADENCE_HOURS", "2")
    assert cfg.write_cadence_hours() == 2
    monkeypatch.setenv("FCM_SHITPOSTER_DRAFT_BATCH_SIZE", "0")
    assert cfg.draft_batch_size() == 5
    monkeypatch.delenv("FCM_SHITPOSTER_DRAFT_EXPIRY_HOURS", raising=False)
    assert cfg.draft_expiry_hours() == 24


def _capture_writer_start(monkeypatch):
    started = []

    class _Session:
        session_id = "sess-1"

    class _FakeClient:
        def supports_pinned_context(self):
            return False

        def start_session(self, agent_id, *, first_turn, pinned_context=None):
            started.append(agent_id)
            return _Session()

    monkeypatch.setattr(write, "get_whagent_client", lambda: _FakeClient())
    monkeypatch.setattr(write, "_wait_for_reply", lambda client, sid, seq, deadline: "[]")
    return started


def test_writer_resolves_definition_name_when_env_unset(monkeypatch):
    monkeypatch.delenv("FCM_SHITPOSTER_WRITER_AGENT_ID", raising=False)
    started = _capture_writer_start(monkeypatch)
    write._call_writer("snapshot", 3)
    assert started == ["shitposter-drafter"]


def test_blank_writer_env_falls_back_to_definition_name(monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_WRITER_AGENT_ID", "   ")
    started = _capture_writer_start(monkeypatch)
    write._call_writer("snapshot", 3)
    assert started == ["shitposter-drafter"]


def test_writer_env_override_takes_precedence(monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_WRITER_AGENT_ID", "custom-drafter")
    started = _capture_writer_start(monkeypatch)
    write._call_writer("snapshot", 3)
    assert started == ["custom-drafter"]


# ---- scheduled post: draft-first path ----


def test_best_ranked_draft_posts_with_its_snapshot_and_is_marked_used(engine, slack, monkeypatch):
    persona_id, snapshot_id, _ = _seed(
        engine,
        drafts=[{"text": "worse", "rank": 2}, {"text": "best", "rank": 1}],
    )
    whagent = FakeWhagent()
    result = _run_scheduled(whagent, monkeypatch)

    assert result.outcome == ShitpostOutcome.POSTED
    assert slack.posts == ["best"]
    assert whagent.starts == []  # no on-the-spot generation
    posts = _posts(engine)
    assert len(posts) == 1
    assert posts[0].context_snapshot_id == snapshot_id
    assert posts[0].whagent_session_id == WRITER_SESSION
    drafts = {d.text: d for d in _drafts(engine)}
    assert drafts["best"].used_at is not None
    assert drafts["best"].used_post_id == posts[0].id
    assert drafts["worse"].used_at is None


def test_queued_draft_links_to_its_writer_session(engine, slack, monkeypatch):
    _seed(engine, drafts=[{"text": "best", "rank": 1}])
    whagent = FakeWhagent()
    whagent.ui_public_url = "https://whagent.example"
    _run_scheduled(whagent, monkeypatch)

    [blocks] = slack.blocks
    assert blocks[1]["elements"][0]["text"] == (
        f"<https://whagent.example/sessions/{WRITER_SESSION}|view prompt>"
    )


def test_draft_is_used_at_most_once_across_runs(engine, slack, monkeypatch):
    _seed(engine, drafts=[{"text": "only", "rank": 1}])
    _run_scheduled(FakeWhagent(), monkeypatch)
    whagent = FakeWhagent(replies=["fallback post"])
    _run_scheduled(whagent, monkeypatch)

    assert slack.posts == ["only", "fallback post"]
    assert len(whagent.starts) == 1
    assert [p.context_snapshot_id for p in _posts(engine)] == [1, 1]
    assert sum(1 for d in _drafts(engine) if d.used_post_id is not None) == 1


def test_expired_draft_is_skipped_and_generation_runs(engine, slack, monkeypatch):
    old = NOW - datetime.timedelta(hours=25)
    _seed(engine, drafts=[{"text": "stale", "rank": 1, "created_at": old}])
    whagent = FakeWhagent(replies=["fresh on the spot"])
    result = _run_scheduled(whagent, monkeypatch)

    assert result.outcome == ShitpostOutcome.POSTED
    assert slack.posts == ["fresh on the spot"]
    [draft] = _drafts(engine)
    assert draft.used_at is None and draft.discarded_at is None


def test_draft_inside_expiry_is_used(engine, slack, monkeypatch):
    recent = NOW - datetime.timedelta(hours=23)
    _seed(engine, drafts=[{"text": "recent", "rank": 1, "created_at": recent}])
    _run_scheduled(FakeWhagent(), monkeypatch)
    assert slack.posts == ["recent"]


@pytest.mark.parametrize("kind", ["attribute_retired", "attribute_family_retired", "lore_retired"])
def test_snapshot_with_retired_item_discards_draft_and_tries_next(engine, slack, monkeypatch, kind):
    with Session(engine) as s:
        persona = s.exec(select(ShitposterPersona)).one()
        if kind == "attribute_retired":
            row = ShitposterPersonaAttribute(
                persona_id=persona.id,
                attribute_key="cheesy",
                text="loves cheese",
                status=ShitposterAttributeStatusEnum.RETIRED.value,
                retired_by_operator=True,
                valid_to=NOW,
            )
            s.add(row)
            s.flush()
            item = ("attribute", row.id)
        elif kind == "attribute_family_retired":
            old = ShitposterPersonaAttribute(
                persona_id=persona.id,
                attribute_key="cheesy",
                text="loves cheese",
                status=ShitposterAttributeStatusEnum.ACTIVE.value,
                valid_to=NOW,
            )
            s.add(old)
            s.flush()
            s.add(
                ShitposterPersonaAttribute(
                    persona_id=persona.id,
                    attribute_key="cheesy",
                    text="loves cheese",
                    status=ShitposterAttributeStatusEnum.RETIRED.value,
                    retired_by_operator=True,
                )
            )
            s.flush()
            item = ("attribute", old.id)
        else:
            row = ShitposterLoreEntry(
                persona_id=persona.id,
                text="cheese joke",
                kind="hit",
                retired_by_operator=True,
                valid_to=NOW,
            )
            s.add(row)
            s.flush()
            item = ("lore", row.id)
        s.commit()
    _seed(
        engine,
        drafts=[{"text": "from retired", "rank": 1}],
        snapshot_items=[item],
    )
    # the second snapshot is clean; its draft should post
    with Session(engine) as s:
        clean = ShitposterContextSnapshot(
            persona_id=s.exec(select(ShitposterPersona)).one().id,
            version=2,
            created_at=NOW,
            brain_job_run_id=s.exec(select(ShitposterBrainJobRun)).first().id,
            token_count=1,
            rendered_text="clean",
        )
        s.add(clean)
        s.flush()
        s.add(
            ShitposterDraft(
                persona_id=clean.persona_id,
                snapshot_id=clean.id,
                brain_job_run_id=clean.brain_job_run_id,
                text="from clean",
                rank=2,
                created_at=NOW,
            )
        )
        s.commit()

    result = _run_scheduled(FakeWhagent(), monkeypatch)
    assert result.outcome == ShitpostOutcome.POSTED
    assert slack.posts == ["from clean"]
    discarded = {d.text: d for d in _drafts(engine) if d.discarded_at is not None}
    assert discarded["from retired"].discard_reason == "retired_item"


def test_draft_failing_guardrail_is_discarded_with_reason(engine, slack, monkeypatch):
    _seed(
        engine,
        drafts=[
            {"text": "hey <@UBOB1> look", "rank": 1},
            {"text": "clean one", "rank": 2},
        ],
    )
    _run_scheduled(FakeWhagent(), monkeypatch)
    assert slack.posts == ["clean one"]
    bad = next(d for d in _drafts(engine) if d.text.startswith("hey"))
    assert bad.discarded_at is not None
    assert bad.discard_reason == "guardrail:mention"
    assert bad.used_at is None


def test_member_name_in_draft_is_discarded(engine, slack, monkeypatch):
    _seed(engine, drafts=[{"text": "bobby tables is lost", "rank": 1}])
    result = _run_scheduled(FakeWhagent(replies=["generated instead"]), monkeypatch)
    assert result.outcome == ShitpostOutcome.POSTED
    assert slack.posts == ["generated instead"]
    [draft] = _drafts(engine)
    assert draft.discard_reason == "guardrail:member_name"


def test_no_drafts_falls_back_to_on_the_spot_generation(engine, slack, monkeypatch):
    whagent = FakeWhagent(replies=["generated"])
    result = _run_scheduled(whagent, monkeypatch)
    assert result.outcome == ShitpostOutcome.POSTED
    assert slack.posts == ["generated"]
    assert len(whagent.starts) == 1
    assert _posts(engine)[0].context_snapshot_id is None


def test_summon_never_consumes_a_queued_draft(engine, slack, monkeypatch):
    _seed(engine, drafts=[{"text": "queued", "rank": 1}])
    whagent = FakeWhagent(replies=["summoned"])
    result = _run_scheduled(whagent, monkeypatch, trigger="summon")
    assert result.outcome == ShitpostOutcome.POSTED
    assert slack.posts == ["summoned"]
    assert len(whagent.starts) == 1
    [draft] = _drafts(engine)
    assert draft.used_at is None and draft.discarded_at is None
