"""Shitposter operator surface: newest-first history, per-kind causes, admin-only
retire/history, and the snapshot-for-post lookup (including the NULL snapshot).

Red-proof. Each mutation was applied, run, and reverted:

  * change_history ordered ascending                -> ordering tests
  * admin check removed from handle_operator_command -> non-admin refusal test
  * retire passes no operator cause ref             -> retire cause test
  * snapshot NULL branch dereferences the snapshot  -> NULL snapshot test
"""

import datetime

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.bot import app as app_mod


class _FakeApp:
    """Stand-in for slack_bolt App: every decorator is a passthrough."""

    def __getattr__(self, _name):
        def _maybe(*a, **k):
            if len(a) == 1 and not k and callable(a[0]):
                return a[0]

            def deco(fn):
                return fn

            return deco

        return _maybe


app_mod._app_instance = _FakeApp()

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    shitposter as handler,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    shitposter_operator as operator,
)
from friendly_computing_machine.src.friendly_computing_machine.db import (  # noqa: E402
    util as db_util,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (  # noqa: E402
    shitposter_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (  # noqa: E402
    shitposter_memory_dal as memory,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base  # noqa: E402
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (  # noqa: E402
    ShitposterBrainJobKind,
    ShitposterBrainJobRun,
    ShitposterBrainJobStatus,
    ShitposterBrainJobTrigger,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterPostEngagement,
    ShitposterSuggestion,
    ShitposterSuggestionBacker,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (  # noqa: E402
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (  # noqa: E402
    ShitposterLoreEntry,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (  # noqa: E402
    SlackChannel,
)

ADMIN = "U_ADMIN"
ADMINS = frozenset({ADMIN})
STRANGER = "U_STRANGER"
CHAN_SLACK_ID = "CONE1"
POST_TS = "1700000000.123456"
PERMALINK = "https://ws.slack.com/archives/CONE1/p1700000000123456"

T0 = datetime.datetime(2026, 10, 1, 12, 0, tzinfo=datetime.UTC)
T1 = T0 + datetime.timedelta(hours=1)
T2 = T0 + datetime.timedelta(hours=2)

TABLES = [
    SlackChannel.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterBrainJobRun.__table__,
    ShitposterContextSnapshot.__table__,
    ShitposterSnapshotItem.__table__,
    ShitposterPost.__table__,
    ShitposterPostEngagement.__table__,
    ShitposterSuggestion.__table__,
    ShitposterSuggestionBacker.__table__,
    ShitposterPersonaAttribute.__table__,
    ShitposterLoreEntry.__table__,
    ShitposterMemoryChange.__table__,
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
def session(engine):
    with Session(engine) as s:
        yield s


@pytest.fixture
def persona(session) -> ShitposterPersona:
    row = ShitposterPersona(name="shitposter")
    session.add(row)
    session.commit()
    session.refresh(row)
    return row


@pytest.fixture
def revision(persona):
    return dal.add_persona_revision(persona.id, "the persona text")


@pytest.fixture
def channel(session) -> SlackChannel:
    row = SlackChannel(slack_id=CHAN_SLACK_ID, name="general", channel_type="public")
    session.add(row)
    session.commit()
    session.refresh(row)
    return row


def _cmd(text, user=ADMIN):
    return {"user_id": user, "channel_id": CHAN_SLACK_ID, "text": text, "team_id": "T1"}


def _changes(persona_id: int) -> list[ShitposterMemoryChange]:
    return memory.change_history(persona_id)


def _ids_in_render(text: str) -> list[int]:
    return [
        int(line[1:].split(" ", 1)[0])
        for line in text.splitlines()
        if line.startswith("#")
    ]


def _naive(ts: datetime.datetime) -> datetime.datetime:
    return ts.replace(tzinfo=None)


# --- history ordering --------------------------------------------------------


def test_history_is_newest_first_by_changed_at_not_insertion(persona):
    # inserted out of time order: the T2 lore add goes in before the T0 attribute add
    attr = memory.add_attribute(
        persona.id, "voice", "dry wit", cause_kind="operator", cause_ref="slack:U_ADMIN", now=T0
    )
    lore = memory.add_lore(
        persona.id,
        "running joke",
        kind="hit",
        cause_kind="operator",
        cause_ref="slack:U_ADMIN",
        now=T2,
    )
    memory.reinforce_attribute(
        attr.id, cause_kind="operator", cause_ref="slack:U_ADMIN", now=T1
    )

    ordered = [c.changed_at for c in _changes(persona.id)]
    assert ordered == sorted(ordered, reverse=True)

    text = operator.render_history(persona.id, 10)
    assert text.splitlines()[0] == "Changes 1-3 of 3, newest first."
    assert _ids_in_render(text) == [
        _latest_change_id(persona.id, lore.id, "add", "lore"),
        _latest_change_id(persona.id, attr.id, "reinforce", "attribute"),
        _latest_change_id(persona.id, attr.id, "add", "attribute"),
    ]


def _latest_change_id(persona_id: int, entity_id: int, operation: str, entity_kind: str) -> int:
    matches = [
        c
        for c in _changes(persona_id)
        if c.entity_id == entity_id
        and c.operation == operation
        and c.entity_kind == entity_kind
    ]
    assert len(matches) == 1
    return matches[0].id


def test_history_paging_takes_offset_from_the_newest_end(persona):
    for i, when in enumerate((T0, T1, T2)):
        memory.add_attribute(
            persona.id,
            f"key{i}",
            f"text{i}",
            cause_kind="operator",
            cause_ref="slack:U_ADMIN",
            now=when,
        )
    text = operator.render_history(persona.id, 2, 2)
    assert text.splitlines()[0] == "Changes 3-3 of 3, newest first."
    assert len(_ids_in_render(text)) == 1
    assert "text0" in text and "text2" not in text


def test_history_past_the_end_reports_total(persona):
    memory.add_attribute(
        persona.id, "voice", "dry", cause_kind="operator", cause_ref="slack:U_ADMIN", now=T0
    )
    assert operator.render_history(persona.id, 5, 9) == "No changes at offset 9 (1 total)."


# --- cause rendering per kind ------------------------------------------------


def test_cause_renders_bot_post_with_engagement(session, persona, revision, channel):
    post = dal.record_post(
        slack_channel_id=channel.id,
        slack_message_ts=POST_TS,
        persona_id=persona.id,
        persona_revision_id=revision.id,
        trigger="scheduled",
        principal_iss="iss",
        principal_sub="sub",
        principal_kind="service",
        whagent_session_id="ws-1",
    )
    session.add(
        ShitposterPostEngagement(
            post_id=post.id,
            persona_id=persona.id,
            finalized_at=T1,
            distinct_reactors=2,
            reactions_by_emoji={"fire": 2, "eyes": 1},
            distinct_repliers=1,
            negative_reactions=0,
        )
    )
    session.commit()
    memory.add_attribute(
        persona.id, "voice", "dry", cause_kind="post_engagement", cause_post_id=post.id, now=T0
    )

    text = operator.render_history(persona.id, 10)
    assert (
        f"cause: bot post {post.id}: 2 reactors (:eyes: x1, :fire: x2), 1 repliers, 0 negative"
        in text
    )


def test_cause_renders_unfinalized_post_without_inventing_engagement(
    persona, revision, channel
):
    post = dal.record_post(
        slack_channel_id=channel.id,
        slack_message_ts=POST_TS,
        persona_id=persona.id,
        persona_revision_id=revision.id,
        trigger="summon",
        principal_iss="iss",
        principal_sub="sub",
        principal_kind="human",
        whagent_session_id="ws-2",
    )
    memory.add_attribute(
        persona.id, "voice", "dry", cause_kind="post_engagement", cause_post_id=post.id, now=T0
    )
    text = operator.render_history(persona.id, 10)
    assert f"bot post {post.id} (engagement not finalized)" in text


def test_cause_renders_suggestion_backers_and_skips_removed(session, persona, channel):
    suggestion = ShitposterSuggestion(
        persona_id=persona.id,
        slack_channel_id=channel.id,
        slack_message_ts="1700000100.000001",
        submitter_slack_user_id="U_SUB",
        text="more cats",
        status="promoted",
        submitted_at=T0,
        expires_at=T0 + datetime.timedelta(days=7),
        status_changed_at=T0,
        promoted_at=T1,
    )
    session.add(suggestion)
    session.commit()
    session.refresh(suggestion)
    session.add(ShitposterSuggestionBacker(suggestion_id=suggestion.id, slack_user_id="U_B1", backed_at=T0))
    session.add(
        ShitposterSuggestionBacker(
            suggestion_id=suggestion.id, slack_user_id="U_B2", backed_at=T0, removed_at=T1
        )
    )
    session.commit()

    memory.add_lore(
        persona.id,
        "cats are weather",
        kind="hit",
        cause_kind="suggestion",
        cause_ref=f"suggestion:{suggestion.id}",
        now=T2,
    )
    text = operator.render_history(persona.id, 10)
    assert (
        f'cause: suggestion {suggestion.id} "more cats" backed by 2: <@U_SUB>, <@U_B1>'
        in text
    )
    assert "U_B2" not in text


def test_cause_renders_operator_and_fold(persona):
    first = memory.add_lore(
        persona.id, "joke a", kind="hit", cause_kind="operator", cause_ref="slack:U_ADMIN", now=T0
    )
    second = memory.add_lore(
        persona.id, "joke b", kind="hit", cause_kind="operator", cause_ref="slack:U_ADMIN", now=T0
    )
    memory.fold_lore([first.id, second.id], "joke ab", cause_ref="fold-run-1", now=T1)

    text = operator.render_history(persona.id, 10)
    assert "cause: operator slack:U_ADMIN" in text
    assert "cause: fold fold-run-1" in text


# --- non-admin refusal -------------------------------------------------------


def _counts(persona_id: int) -> tuple[int, int, int]:
    with Session(db_util.get_engine()) as s:
        return (
            len(s.exec(select(ShitposterMemoryChange)).all()),
            len(s.exec(select(ShitposterPersonaAttribute)).all()),
            len(s.exec(select(ShitposterLoreEntry)).all()),
        )


def test_non_admin_is_refused_and_writes_nothing(persona):
    attr = memory.add_attribute(
        persona.id, "voice", "dry", cause_kind="operator", cause_ref="slack:U_ADMIN", now=T0
    )
    lore = memory.add_lore(
        persona.id, "joke", kind="hit", cause_kind="operator", cause_ref="slack:U_ADMIN", now=T0
    )
    before = _counts(persona.id)

    for text in (
        "history",
        "runs",
        f"retire attribute {attr.id}",
        f"retire lore {lore.id}",
        f"snapshot {PERMALINK}",
    ):
        assert (
            operator.handle_operator_command(text, STRANGER, ADMINS)
            == operator.NOT_ADMIN
        )
        reply, notice = handler.handle_shitpost(
            _cmd(f"admin {text}", user=STRANGER), ADMINS
        )
        assert reply == operator.NOT_ADMIN
        assert notice is None

    assert _counts(persona.id) == before
    assert [r.id for r in memory.active_attributes(persona.id)] == [attr.id]
    assert [r.id for r in memory.active_lore(persona.id)] == [lore.id]


# --- retire ------------------------------------------------------------------


def test_admin_retire_attribute_logs_operator_cause_and_closes_row(session, persona):
    attr = memory.add_attribute(
        persona.id,
        "voice",
        "dry wit",
        cause_kind="suggestion",
        cause_ref="suggestion:1",
        now=T0,
    )
    reply, notice = handler.handle_shitpost(
        _cmd(f"admin retire attribute {attr.id}"), ADMINS
    )
    assert notice is None
    assert reply.startswith(f"Retired attribute {attr.id}")

    change = _changes(persona.id)[0]
    assert change.operation == "retire"
    assert change.entity_kind == "attribute"
    assert change.entity_id == attr.id
    assert change.cause_kind == "operator"
    assert change.cause_ref == f"slack:{ADMIN}"
    assert change.cause_post_id is None
    assert change.before_text == "dry wit"
    assert change.after_text is None

    rows = list(
        session.exec(
            select(ShitposterPersonaAttribute)
            .where(ShitposterPersonaAttribute.attribute_key == "voice")
            .order_by(ShitposterPersonaAttribute.id)
        ).all()
    )
    assert len(rows) == 2
    assert rows[0].valid_to is not None
    assert _naive(rows[0].valid_to) == _naive(change.changed_at)
    assert rows[1].status == "retired"
    assert rows[1].retired_by_operator is True
    assert rows[1].valid_to is None
    assert memory.active_attributes(persona.id) == []


def test_admin_retire_lore_closes_row_with_operator_cause(session, persona):
    lore = memory.add_lore(
        persona.id,
        "running joke",
        kind="hit",
        cause_kind="operator",
        cause_ref="slack:U_ADMIN",
        now=T0,
    )
    reply, _ = handler.handle_shitpost(_cmd(f"admin retire lore {lore.id}"), ADMINS)
    assert reply.startswith(f"Retired lore {lore.id}")

    row = session.get(ShitposterLoreEntry, lore.id)
    session.refresh(row)
    assert row.valid_to is not None
    assert row.retired_by_operator is True
    assert memory.active_lore(persona.id) == []

    change = _changes(persona.id)[0]
    assert change.entity_kind == "lore"
    assert change.cause_kind == "operator"
    assert change.cause_ref == f"slack:{ADMIN}"


def test_retire_of_a_non_current_row_writes_nothing(persona):
    attr = memory.add_attribute(
        persona.id, "voice", "dry", cause_kind="operator", cause_ref="slack:U_ADMIN", now=T0
    )
    memory.retire_attribute(
        attr.id, cause_kind="operator", cause_ref="slack:U_ADMIN", retired_by_operator=True, now=T1
    )
    before = len(_changes(persona.id))

    reply, _ = handler.handle_shitpost(_cmd(f"admin retire attribute {attr.id}"), ADMINS)
    assert reply.startswith("Retire failed")
    assert len(_changes(persona.id)) == before


# --- snapshot for post -------------------------------------------------------


def _brain_run(session, persona) -> ShitposterBrainJobRun:
    run = ShitposterBrainJobRun(
        persona_id=persona.id,
        job_kind=ShitposterBrainJobKind.SNAPSHOT.value,
        trigger=ShitposterBrainJobTrigger.OPERATOR.value,
        status=ShitposterBrainJobStatus.SUCCEEDED.value,
    )
    session.add(run)
    session.commit()
    session.refresh(run)
    return run


def _snapshot(session, persona, run, version=1) -> ShitposterContextSnapshot:
    snap = ShitposterContextSnapshot(
        persona_id=persona.id,
        version=version,
        brain_job_run_id=run.id,
        token_count=42,
        rendered_text="PERSONA: dry wit",
        created_at=T0,
    )
    session.add(snap)
    session.commit()
    session.refresh(snap)
    return snap


def _post(persona, revision, channel, ts: str, snapshot_id: int | None, ws: str):
    return dal.record_post(
        slack_channel_id=channel.id,
        slack_message_ts=ts,
        persona_id=persona.id,
        persona_revision_id=revision.id,
        trigger="scheduled",
        principal_iss="iss",
        principal_sub="sub",
        principal_kind="service",
        whagent_session_id=ws,
        context_snapshot_id=snapshot_id,
    )


def test_parse_permalink_extracts_channel_and_ts():
    assert operator.parse_permalink(f"<{PERMALINK}|link>") == ("CONE1", POST_TS)
    assert operator.parse_permalink("not a link") is None


def test_snapshot_for_post_returns_the_snapshot_it_was_written_from(
    session, persona, revision, channel
):
    run = _brain_run(session, persona)
    snap = _snapshot(session, persona, run)
    _post(persona, revision, channel, POST_TS, snap.id, "ws-a")

    reply, notice = handler.handle_shitpost(_cmd(f"admin snapshot {PERMALINK}"), ADMINS)
    assert notice is None
    assert f"was written from snapshot {snap.id} (v1, 42 tokens" in reply
    assert "PERSONA: dry wit" in reply


def test_snapshot_for_post_with_null_snapshot_reports_it(
    session, persona, revision, channel
):
    run = _brain_run(session, persona)
    _snapshot(session, persona, run)
    _post(persona, revision, channel, POST_TS, None, "ws-b")

    reply = operator.render_snapshot_for_post(PERMALINK)
    assert "has no recorded snapshot" in reply
    assert "Latest snapshot is v1." in reply
    assert "was written from" not in reply


def test_snapshot_for_post_null_with_no_snapshots_for_persona(
    session, revision, channel
):
    other = ShitposterPersona(name="other")
    session.add(other)
    session.commit()
    session.refresh(other)
    other_rev = dal.add_persona_revision(other.id, "other text")
    _post(other, other_rev, channel, POST_TS, None, "ws-c")

    reply = operator.render_snapshot_for_post(PERMALINK)
    assert "has no recorded snapshot" in reply
    assert "Latest snapshot" not in reply


def test_snapshot_lookup_rejects_bad_and_unknown_links(persona, revision, channel):
    assert operator.render_snapshot_for_post("hello") == "That is not a Slack message permalink."
    unknown = "https://ws.slack.com/archives/CONE1/p1700000999000001"
    assert operator.render_snapshot_for_post(unknown) == "No Shitposter post matches that permalink."
