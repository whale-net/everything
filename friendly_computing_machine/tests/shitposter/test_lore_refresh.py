"""Earned popularity refresh for generated lore, and identity across refresh revisions.

Runs lore_refresh compute/apply and the idea-generator job body against an
in-memory SQLite DB. Posts are tied to entries through context snapshots.

Red-proof: matching snapshot items by row id instead of lore_key in
_contributing_posts turns the cross-revision test red; reverting operator
retire or snapshot_has_retired_item to the row id turns the retire tests red.
"""

import datetime
import json

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_draft_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobRun,
    ShitposterChannelOptIn,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
    ShitposterSourceWeight,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    idea_generate,
    lore_refresh,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    BrainJobInput,
)

RUN_ID = 1
NOW = datetime.datetime.now(datetime.timezone.utc)
TABLES = [
    SlackUser.__table__,
    SlackChannel.__table__,
    SlackMessage.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterChannelOptIn.__table__,
    ShitposterBrainJobRun.__table__,
    ShitposterContextSnapshot.__table__,
    ShitposterSnapshotItem.__table__,
    ShitposterPost.__table__,
    ShitposterPostEngagement.__table__,
    ShitposterPersonaAttribute.__table__,
    ShitposterLoreEntry.__table__,
    ShitposterMemoryChange.__table__,
    ShitposterSourceWeight.__table__,
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
    for name in (
        "FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP",
        "FCM_SHITPOSTER_THEME_TARGET_WEIGHT",
        "FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT",
        "FCM_SHITPOSTER_NOVEL_GATE_RECENT_DAYS",
        "FCM_SHITPOSTER_NOVEL_GATE_BASELINE_DAYS",
        "FCM_SHITPOSTER_NOVEL_GATE_FRACTION",
    ):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setattr(idea_generate, "_member_names", lambda: ["Alice"])
    return engine


@pytest.fixture
def persona_id(engine) -> int:
    with Session(engine) as s:
        persona = ShitposterPersona(name="shitposter")
        s.add(persona)
        s.commit()
        return persona.id


def _lore(engine, persona_id, kind, text, popularity=0) -> int:
    with Session(engine) as s:
        row = ShitposterLoreEntry(
            persona_id=persona_id, text=text, kind=kind, popularity_score=popularity
        )
        s.add(row)
        s.commit()
        return row.id


_versions = iter(range(1, 100000))


def _snapshot(engine, persona_id, lore_ids) -> int:
    with Session(engine) as s:
        snap = ShitposterContextSnapshot(
            persona_id=persona_id,
            version=next(_versions),
            created_at=NOW,
            brain_job_run_id=RUN_ID,
            token_count=0,
            rendered_text="",
        )
        s.add(snap)
        s.flush()
        for rank, lore_id in enumerate(lore_ids, start=1):
            s.add(
                ShitposterSnapshotItem(
                    snapshot_id=snap.id, item_kind="lore", item_id=lore_id, rank=rank
                )
            )
        s.commit()
        return snap.id


_ts = iter(range(1_700_000_000, 1_800_000_000))


def _post(engine, persona_id, snapshot_id, reactors=None, repliers=0) -> int:
    """A bot post written from snapshot_id; finalized only when reactors is given."""
    with Session(engine) as s:
        post = ShitposterPost(
            slack_channel_id=1,
            slack_message_ts=f"{next(_ts)}.000000",
            persona_id=persona_id,
            persona_revision_id=1,
            trigger="scheduled",
            principal_iss="iss",
            principal_sub="sub",
            principal_kind="service",
            whagent_session_id="sess",
            context_snapshot_id=snapshot_id,
        )
        s.add(post)
        s.flush()
        if reactors is not None:
            s.add(
                ShitposterPostEngagement(
                    post_id=post.id,
                    persona_id=persona_id,
                    finalized_at=NOW,
                    distinct_reactors=reactors,
                    reactions_by_emoji={},
                    distinct_repliers=repliers,
                    negative_reactions=0,
                )
            )
        s.commit()
        return post.id


def _refresh(engine, persona_id) -> list[dict]:
    with Session(engine) as s:
        planned = lore_refresh.compute_refreshes(s, persona_id)
    with Session(engine) as s:
        details = lore_refresh.apply_refreshes(s, planned, datetime.datetime.now(datetime.timezone.utc))
        s.commit()
    return details


def _current(engine, lore_key) -> ShitposterLoreEntry:
    with Session(engine) as s:
        return s.exec(
            select(ShitposterLoreEntry)
            .where(ShitposterLoreEntry.lore_key == lore_key)
            .where(ShitposterLoreEntry.valid_to.is_(None))
        ).one()


def _row(engine, lore_id) -> ShitposterLoreEntry:
    with Session(engine) as s:
        return s.get(ShitposterLoreEntry, lore_id)


def _refresh_changes(engine) -> list[ShitposterMemoryChange]:
    with Session(engine) as s:
        return list(
            s.exec(
                select(ShitposterMemoryChange).where(
                    ShitposterMemoryChange.operation == "refresh"
                )
            ).all()
        )


# --- earned popularity --------------------------------------------------------


def test_entry_with_posts_gets_their_mean_and_cause_naming_them(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods")
    other = _lore(engine, persona_id, "hit", "unrelated hit")
    with_theme = _snapshot(engine, persona_id, [theme, other])
    without_theme = _snapshot(engine, persona_id, [other])
    p1 = _post(engine, persona_id, with_theme, reactors=3, repliers=1)  # 4
    p2 = _post(engine, persona_id, with_theme, reactors=2)  # 2
    _post(engine, persona_id, with_theme)  # not finalized
    _post(engine, persona_id, without_theme, reactors=100)
    key = _row(engine, theme).lore_key

    details = _refresh(engine, persona_id)

    current = _current(engine, key)
    assert current.id != theme and current.popularity_score == 3
    assert _row(engine, theme).valid_to is not None
    assert details == [
        {"lore_id": current.id, "lore_key": str(key), "old": 0, "new": 3, "post_count": 2}
    ]
    [change] = _refresh_changes(engine)
    assert (change.entity_kind, change.entity_id, change.cause_kind) == (
        "lore",
        theme,
        "post_engagement",
    )
    assert change.cause_ref == f"post:{p1},post:{p2}"


def test_novel_entry_is_refreshed_too(engine, persona_id):
    novel = _lore(engine, persona_id, "novel", "Thinks the moon is a rumor", popularity=1)
    _post(engine, persona_id, _snapshot(engine, persona_id, [novel]), reactors=5)
    key = _row(engine, novel).lore_key

    _refresh(engine, persona_id)

    assert _current(engine, key).popularity_score == 5


def test_no_posts_means_no_change(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods", popularity=7)
    # in a snapshot, but the only post from it has not finalized
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]))

    assert _refresh(engine, persona_id) == []

    row = _row(engine, theme)
    assert row.valid_to is None and row.popularity_score == 7
    assert _refresh_changes(engine) == []


def test_hit_lore_is_not_refreshed(engine, persona_id):
    hit = _lore(engine, persona_id, "hit", "the goose incident", popularity=1)
    _post(engine, persona_id, _snapshot(engine, persona_id, [hit]), reactors=9)

    assert _refresh(engine, persona_id) == []
    assert _row(engine, hit).valid_to is None


def test_earned_equal_to_current_writes_nothing(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods", popularity=4)
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=4)

    assert _refresh(engine, persona_id) == []
    assert _row(engine, theme).valid_to is None


def test_posts_span_revisions_and_count_once(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods")
    key = _row(engine, theme).lore_key
    p1 = _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=6)
    _refresh(engine, persona_id)
    rev2 = _current(engine, key).id
    assert _row(engine, rev2).popularity_score == 6

    # one post from a snapshot of the new revision, one from a snapshot holding both
    p2 = _post(engine, persona_id, _snapshot(engine, persona_id, [rev2]), reactors=0)
    p3 = _post(engine, persona_id, _snapshot(engine, persona_id, [theme, rev2]), reactors=3)

    with Session(engine) as s:
        assert lore_refresh.contributing_post_ids(s, key) == [p1, p2, p3]
    details = _refresh(engine, persona_id)

    assert _current(engine, key).popularity_score == 3  # mean(6, 0, 3)
    assert details[0]["post_count"] == 3 and details[0]["old"] == 6
    latest = max(_refresh_changes(engine), key=lambda c: c.id)
    assert latest.entity_id == rev2
    assert latest.cause_ref == f"post:{p1},post:{p2},post:{p3}"


def test_apply_skips_entry_retired_since_compute(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods")
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=5)
    with Session(engine) as s:
        planned = lore_refresh.compute_refreshes(s, persona_id)
    assert len(planned) == 1
    dal.operator_retire("lore", theme, "U_ADMIN")

    with Session(engine) as s:
        assert lore_refresh.apply_refreshes(s, planned, NOW) == []
        s.commit()
    assert _refresh_changes(engine) == []


# --- identity after a refresh -------------------------------------------------


def test_refresh_keeps_first_revision_valid_from(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods")
    first = _row(engine, theme)
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=5)
    _refresh(engine, persona_id)

    current = _current(engine, first.lore_key)
    assert current.valid_from > first.valid_from
    with Session(engine) as s:
        assert dal.first_valid_from_by_key(s, persona_id, [first.lore_key]) == {
            first.lore_key: first.valid_from
        }


def test_retire_after_refresh_affects_drafts_from_earlier_revisions(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods")
    key = _row(engine, theme).lore_key
    old_snapshot = _snapshot(engine, persona_id, [theme])
    _post(engine, persona_id, old_snapshot, reactors=5)
    _refresh(engine, persona_id)
    with Session(engine) as s:
        assert not shitposter_draft_dal.snapshot_has_retired_item(s, old_snapshot)

    dal.operator_retire("lore", _current(engine, key).id, "U_ADMIN")

    with Session(engine) as s:
        assert shitposter_draft_dal.snapshot_has_retired_item(s, old_snapshot)


def test_operator_retire_by_earlier_revision_id_retires_the_entry(engine, persona_id):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods")
    key = _row(engine, theme).lore_key
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=5)
    _refresh(engine, persona_id)
    rev2 = _current(engine, key).id

    change = dal.operator_retire("lore", theme, "U_ADMIN")

    assert change.entity_id == rev2
    row = _row(engine, rev2)
    assert row.valid_to is not None and row.retired_by_operator


# --- inside an idea-generator run ---------------------------------------------


def _theme_at_target(engine, persona_id) -> int:
    for i in range(17):
        _lore(engine, persona_id, "hit", f"hit joke number {i}")
    themes = [_lore(engine, persona_id, "theme", f"theme {i}") for i in range(3)]
    return themes[0]


def _run(engine, persona_id):
    payload = idea_generate._compute(
        BrainJobInput(persona_id=persona_id, job_kind="idea_generate")
    )
    with Session(engine) as s:
        outcome = idea_generate._apply(s, RUN_ID, payload)
        s.commit()
    return outcome


def _no_novel_agent(monkeypatch):
    def boom(*_):
        raise AssertionError("novel agent must not be called")

    monkeypatch.setattr(idea_generate, "_call_novel_agent", boom)


def test_run_records_refreshes_in_details(engine, persona_id, monkeypatch):
    theme = _theme_at_target(engine, persona_id)
    key = _row(engine, theme).lore_key
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=4)
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=2)

    def no_theme(*_):
        raise AssertionError("theme agent must not be called at target")

    monkeypatch.setattr(idea_generate, "_call_theme_agent", no_theme)
    _no_novel_agent(monkeypatch)

    outcome = _run(engine, persona_id)

    assert outcome.status == "succeeded"
    assert outcome.details["entries"] == []
    current = _current(engine, key)
    assert outcome.details["refreshes"] == [
        {"lore_id": current.id, "lore_key": str(key), "old": 0, "new": 3, "post_count": 2}
    ]
    assert current.popularity_score == 3
    json.dumps(outcome.details)


def test_failed_run_writes_no_refresh(engine, persona_id, monkeypatch):
    theme = _lore(engine, persona_id, "theme", "Fixated on breakfast foods")
    for i in range(17):  # 1/18 theme share sits below target, so the agent is asked
        _lore(engine, persona_id, "hit", f"hit joke number {i}")
    _post(engine, persona_id, _snapshot(engine, persona_id, [theme]), reactors=5)

    def broken(*_):
        raise RuntimeError("agent unavailable")

    monkeypatch.setattr(idea_generate, "_call_theme_agent", broken)
    _no_novel_agent(monkeypatch)

    outcome = _run(engine, persona_id)

    assert outcome.status == "failed"
    assert outcome.details["refreshes"] == []
    row = _row(engine, theme)
    assert row.valid_to is None and row.popularity_score == 0
    assert _refresh_changes(engine) == []
