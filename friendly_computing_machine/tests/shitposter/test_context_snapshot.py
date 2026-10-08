"""Context snapshot brain job: budget, random pick, retirement, versioning, and text sources.

Pure selection tests call select_items directly. DB tests run the job body's
apply step against SQLite with real memory DAL writes, so retired rows and
version numbers come from the same tables production reads.

Red-proof: making select_items skip the budget check, or dropping the
retired filter in active_lore, turns the matching tests red.
"""

import datetime
import random

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as memory,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_snapshot_dal as snapshot_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobRun,
    ShitposterBrainJobStatus,
    ShitposterBrainJobTrigger,
    ShitposterPersona,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    snapshot,
)

T0 = datetime.datetime(2026, 10, 1, 12, 0, tzinfo=datetime.UTC)
SUGGESTION_MARKER = "SUGGESTION-RAW-MARKER-7f3a"
TABLES = [
    ShitposterPersona.__table__,
    ShitposterBrainJobRun.__table__,
    ShitposterContextSnapshot.__table__,
    ShitposterSnapshotItem.__table__,
    ShitposterPersonaAttribute.__table__,
    ShitposterLoreEntry.__table__,
    ShitposterMemoryChange.__table__,
]


def _cand(kind: str, item_id: int, text: str, score: float = 0.0) -> snapshot.Candidate:
    return snapshot.Candidate(kind, item_id, text, score)


def _line_tokens(*texts: str) -> int:
    return snapshot.approx_tokens("\n".join(snapshot.render_line(t) for t in texts))


# --- pure selection -------------------------------------------------------


@pytest.mark.parametrize("budget", [1, 5, 11, 20, 50, 200])
def test_budget_never_exceeded(budget):
    attrs = [_cand("attribute", i, f"attr {i} " * 3) for i in range(5)]
    lore = [_cand("lore", 100 + i, f"lore {i} " * 4, score=1.0 - i * 0.1) for i in range(12)]
    result = snapshot.select_items(attrs, lore, budget, random.Random(1))
    assert snapshot.approx_tokens(result.rendered_text) <= budget
    assert result.token_count == snapshot.approx_tokens(result.rendered_text)


def test_attributes_alone_near_budget_block_lore():
    # one attribute is exactly at budget; lore must not push it over
    text = "x" * 38  # line "- " + 38 = 40 chars -> 10 tokens
    attrs = [_cand("attribute", 1, text)]
    lore = [_cand("lore", 2, "y" * 4, score=5.0)]
    budget = snapshot.approx_tokens(snapshot.render_line(text))
    result = snapshot.select_items(attrs, lore, budget, random.Random(0))
    assert [i.item_kind for i in result.items] == ["attribute"]
    assert result.token_count <= budget


def test_attribute_that_does_not_fit_is_dropped_not_overflowed():
    attrs = [_cand("attribute", 1, "a" * 100), _cand("attribute", 2, "b")]
    result = snapshot.select_items(attrs, [], 3, random.Random(0))
    assert snapshot.approx_tokens(result.rendered_text) <= 3
    assert [i.item_id for i in result.items] == [2]


def test_lore_ranked_by_score_then_id():
    lore = [
        _cand("lore", 7, "low", score=0.1),
        _cand("lore", 3, "high", score=0.9),
        _cand("lore", 5, "tie-b", score=0.5),
        _cand("lore", 4, "tie-a", score=0.5),
    ]
    result = snapshot.select_items([], lore, 10_000, random.Random(0))
    assert [i.item_id for i in result.items if not i.is_random_pick] == [3, 4, 5, 7]


def test_random_pick_dropped_first_when_budget_is_tight():
    attr = _cand("attribute", 1, "a" * 18)
    top = _cand("lore", 2, "b" * 18, score=0.9)
    rest = [_cand("lore", 3, "c" * 18, score=0.1)]
    budget = _line_tokens("a" * 18, "b" * 18)  # attribute + top lore fill it exactly
    result = snapshot.select_items([attr], [top, *rest], budget, random.Random(0))
    assert not any(i.is_random_pick for i in result.items)
    assert [i.item_id for i in result.items] == [1, 2]
    assert snapshot.approx_tokens(result.rendered_text) <= budget


def test_random_pick_is_lower_ranked_and_marked_when_it_fits():
    attr = _cand("attribute", 1, "a")
    lore = [
        _cand("lore", 2, "top one", score=0.9),
        _cand("lore", 3, "second", score=0.5),
        _cand("lore", 4, "third", score=0.2),
        _cand("lore", 5, "fourth", score=0.1),
    ]
    result = snapshot.select_items([attr], lore, 10_000, random.Random(0), ranked_lore_cap=2)
    ranked = [i.item_id for i in result.items if not i.is_random_pick and i.item_kind == "lore"]
    assert ranked == [2, 3]
    picks = [i for i in result.items if i.is_random_pick]
    assert len(picks) == 1
    assert picks[0].item_kind == "lore"
    # a pick is drawn only from lore ranked below the cap
    assert picks[0].item_id in {4, 5}


def test_no_random_pick_when_lore_fits_within_ranked_cap():
    lore = [
        _cand("lore", 2, "top one", score=0.9),
        _cand("lore", 3, "second", score=0.5),
    ]
    result = snapshot.select_items([], lore, 10_000, random.Random(0), ranked_lore_cap=2)
    assert not any(i.is_random_pick for i in result.items)
    assert [i.item_id for i in result.items] == [2, 3]


# --- DB-backed job body ---------------------------------------------------


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
    monkeypatch.setattr(snapshot, "RNG_FACTORY", lambda: random.Random(42))
    return engine


def _persona_and_run(session: Session) -> tuple[int, int]:
    persona = ShitposterPersona(name="shitposter")
    session.add(persona)
    session.flush()
    run = ShitposterBrainJobRun(
        persona_id=persona.id,
        job_kind=ShitposterBrainJobKind.SNAPSHOT.value,
        trigger=ShitposterBrainJobTrigger.OPERATOR.value,
        status=ShitposterBrainJobStatus.RUNNING.value,
    )
    session.add(run)
    session.flush()
    return persona.id, run.id


def _run_snapshot(engine, persona_id: int, run_id: int) -> dict:
    with Session(engine) as s:
        outcome = snapshot._apply(s, run_id, {"persona_id": persona_id})
        # the runner closes the run; the running-lock index forbids a second running row
        s.get(ShitposterBrainJobRun, run_id).status = outcome.status
        s.commit()
    assert outcome.status == ShitposterBrainJobStatus.SUCCEEDED.value
    return outcome.details


def test_db_snapshot_retired_items_absent_and_versions_increment(engine, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_CONTEXT_TOKEN_BUDGET", "2000")
    with Session(engine) as s:
        persona_id, run1 = _persona_and_run(s)
        memory.add_attribute(
            persona_id, "voice", "dry and terse", cause_kind="operator",
            cause_ref="op:1", now=T0, session=s,
        )
        keep = memory.add_lore(
            persona_id, "the goose incident", kind="hit",
            cause_kind="post_engagement", cause_post_id=None, cause_ref="post:1",
            now=T0, session=s,
        )
        gone = memory.add_lore(
            persona_id, "RETIRED-LORE-ENTRY", kind="hit",
            cause_kind="operator", cause_ref="op:2", now=T0, session=s,
        )
        s.commit()
        keep_id, gone_id = keep.id, gone.id
        memory.retire_lore(
            gone_id, cause_kind="operator", cause_ref="op:3",
            retired_by_operator=True, now=T0, session=s,
        )
        s.commit()

    first = _run_snapshot(engine, persona_id, run1)
    with Session(engine) as s:
        run2 = ShitposterBrainJobRun(
            persona_id=persona_id,
            job_kind=ShitposterBrainJobKind.SNAPSHOT.value,
            trigger="operator",
            status=ShitposterBrainJobStatus.RUNNING.value,
        )
        s.add(run2)
        s.commit()
        run2_id = run2.id
    second = _run_snapshot(engine, persona_id, run2_id)

    assert first["version"] == 1
    assert second["version"] == 2

    with Session(engine) as s:
        latest = snapshot_dal.latest_snapshot(persona_id, session=s)
        assert latest.version == 2
        assert "RETIRED-LORE-ENTRY" not in latest.rendered_text
        assert "dry and terse" in latest.rendered_text
        assert "the goose incident" in latest.rendered_text
        item_ids = {
            (i.item_kind, i.item_id)
            for i in snapshot_dal.snapshot_items(latest.id, session=s)
        }
        assert ("lore", gone_id) not in item_ids
        assert ("lore", keep_id) in item_ids


def test_old_versions_stay_readable(engine):
    with Session(engine) as s:
        persona_id, run1 = _persona_and_run(s)
        memory.add_attribute(
            persona_id, "voice", "first voice", cause_kind="operator",
            cause_ref="op:1", now=T0, session=s,
        )
        s.commit()
    first = _run_snapshot(engine, persona_id, run1)

    with Session(engine) as s:
        memory.add_lore(
            persona_id, "new lore after v1", kind="hit",
            cause_kind="operator", cause_ref="op:9", now=T0, session=s,
        )
        run2 = ShitposterBrainJobRun(
            persona_id=persona_id,
            job_kind=ShitposterBrainJobKind.SNAPSHOT.value,
            trigger="operator",
            status=ShitposterBrainJobStatus.RUNNING.value,
        )
        s.add(run2)
        s.commit()
        run2_id = run2.id
    _run_snapshot(engine, persona_id, run2_id)

    with Session(engine) as s:
        old = snapshot_dal.snapshot_by_id(first["snapshot_id"], session=s)
        assert old.version == 1
        assert old.rendered_text == "- first voice"
        assert "new lore after v1" not in old.rendered_text
        old_items = snapshot_dal.snapshot_items(old.id, session=s)
        assert [i.item_kind for i in old_items] == ["attribute"]


def test_snapshot_contains_no_suggestion_text(engine):
    # raw suggestion text is stored only on a memory change cause, never as a row the job reads
    with Session(engine) as s:
        persona_id, run_id = _persona_and_run(s)
        memory.add_attribute(
            persona_id, "voice", "attribute derived from feedback", cause_kind="suggestion",
            cause_ref=SUGGESTION_MARKER, now=T0, session=s,
        )
        memory.add_lore(
            persona_id, "lore line", kind="hit", cause_kind="suggestion",
            cause_ref=SUGGESTION_MARKER, now=T0, session=s,
        )
        s.commit()
    details = _run_snapshot(engine, persona_id, run_id)

    with Session(engine) as s:
        latest = snapshot_dal.latest_snapshot(persona_id, session=s)
        assert SUGGESTION_MARKER not in latest.rendered_text
        allowed = {
            f"- {r.text}"
            for r in s.exec(select(ShitposterPersonaAttribute)).all()
        } | {f"- {r.text}" for r in s.exec(select(ShitposterLoreEntry)).all()}
        for line in latest.rendered_text.split("\n"):
            assert line in allowed
    assert details["item_count"] == 2
