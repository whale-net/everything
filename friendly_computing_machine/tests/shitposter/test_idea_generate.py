"""Idea-generator job body: theme branch, pool targeting, run cap, guardrails.

Runs _compute/_apply against an in-memory SQLite DB with the theme agent mocked.
"""

import datetime
import json

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterChannelOptIn,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    idea_generate,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    BrainJobInput,
)

RUN_ID = 1
TABLES = [
    SlackUser.__table__,
    SlackChannel.__table__,
    SlackMessage.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterChannelOptIn.__table__,
    ShitposterPost.__table__,
    ShitposterPostEngagement.__table__,
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
    for name in (
        "FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP",
        "FCM_SHITPOSTER_THEME_TARGET_WEIGHT",
        "FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT",
        "FCM_SHITPOSTER_THEME_GENERATOR_AGENT_ID",
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


def _lore(engine, persona_id, kind, text, popularity=0, retired=False) -> int:
    with Session(engine) as s:
        row = ShitposterLoreEntry(
            persona_id=persona_id,
            text=text,
            kind=kind,
            popularity_score=popularity,
        )
        if retired:
            row.retired_by_operator = True
            row.valid_to = datetime.datetime.now(datetime.timezone.utc)
        s.add(row)
        s.commit()
        return row.id


def _agent(monkeypatch, entries, calls=None):
    def fake(active, retired, max_entries):
        if calls is not None:
            calls.append({"active": active, "retired": retired, "max": max_entries})
        return json.dumps({"entries": entries})

    monkeypatch.setattr(idea_generate, "_call_theme_agent", fake)


def _no_agent(monkeypatch):
    def boom(*_):
        raise AssertionError("theme agent must not be called at or above target")

    monkeypatch.setattr(idea_generate, "_call_theme_agent", boom)


def _run(engine, persona_id):
    payload = idea_generate._compute(
        BrainJobInput(persona_id=persona_id, job_kind="idea_generate")
    )
    with Session(engine) as s:
        outcome = idea_generate._apply(s, RUN_ID, payload)
        s.commit()
    return payload, outcome


def _themes(engine, persona_id):
    return [r for r in dal.active_lore(persona_id) if r.kind == "theme"]


def _hits(engine, persona_id, n, popularity=0):
    return [
        _lore(engine, persona_id, "hit", f"hit joke number {i}", popularity=popularity)
        for i in range(n)
    ]


def test_theme_below_target_emits_theme_entry(engine, persona_id, monkeypatch):
    a, b, *_ = _hits(engine, persona_id, 4)
    _agent(monkeypatch, [{"text": "Fixated on breakfast foods", "cites": [a, b]}])
    payload, outcome = _run(engine, persona_id)

    assert outcome.status == "succeeded"
    assert payload["theme"] == {"share": 0.0, "target": 0.15}
    assert payload["novel"]["gate"] == "not_implemented"
    themes = _themes(engine, persona_id)
    assert [t.text for t in themes] == ["Fixated on breakfast foods"]
    entry = outcome.details["entries"][0]
    assert entry["lore_id"] == themes[0].id and entry["kind"] == "theme"
    assert entry["cited"] == [a, b] and entry["agent_id"]
    assert outcome.details["refreshes"] == [] and outcome.details["weight_changes"] == []

    with Session(engine) as s:
        change = s.exec(
            select(ShitposterMemoryChange).where(
                ShitposterMemoryChange.entity_id == themes[0].id
            )
        ).one()
    assert (change.entity_kind, change.operation, change.cause_kind) == (
        "lore",
        "add",
        "theme_synthesis",
    )
    assert change.cause_ref == f"lore:{a},lore:{b}"


def test_theme_at_target_is_no_op(engine, persona_id, monkeypatch):
    hits = _hits(engine, persona_id, 17)
    for i in range(3):  # 3/20 = 0.15, exactly at target
        _lore(engine, persona_id, "theme", f"theme {i}")
    _no_agent(monkeypatch)
    payload, outcome = _run(engine, persona_id)
    assert outcome.status == "no_op"
    assert payload["theme"]["share"] == pytest.approx(0.15)
    assert outcome.details["entries"] == []
    assert len(_themes(engine, persona_id)) == 3
    assert hits


def test_empty_pool_counts_as_below_target(engine, persona_id, monkeypatch):
    calls: list = []
    _agent(monkeypatch, [{"text": "Loves trains", "cites": []}], calls)
    payload, outcome = _run(engine, persona_id)
    assert len(calls) == 1
    assert payload["theme"]["share"] is None
    # nothing to cite in an empty pool, so the proposal is rejected
    assert outcome.status == "no_op"
    assert outcome.details["rejections"][0]["reason"] == "insufficient_citations"


def test_run_cap_holds(engine, persona_id, monkeypatch):
    a, b, c, *_ = _hits(engine, persona_id, 6)
    _agent(
        monkeypatch,
        [
            {"text": "Obsessed with weather", "cites": [a, b]},
            {"text": "Distrusts pigeons deeply", "cites": [b, c]},
            {"text": "Collects broken umbrellas", "cites": [a, c]},
        ],
    )
    _, outcome = _run(engine, persona_id)
    assert len(_themes(engine, persona_id)) == 2
    assert [r["reason"] for r in outcome.details["rejections"]] == ["run_cap"]


def test_configured_run_cap(engine, persona_id, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP", "1")
    a, b, *_ = _hits(engine, persona_id, 4)
    _agent(
        monkeypatch,
        [
            {"text": "Obsessed with weather", "cites": [a, b]},
            {"text": "Distrusts pigeons deeply", "cites": [a, b]},
        ],
    )
    _run(engine, persona_id)
    assert len(_themes(engine, persona_id)) == 1


@pytest.mark.parametrize(
    "cites_fn",
    [
        lambda a, b, retired: [a],
        lambda a, b, retired: [a, a],
        lambda a, b, retired: [a, 9999],
        lambda a, b, retired: [a, retired],
    ],
    ids=["one", "duplicate", "unknown", "inactive"],
)
def test_fewer_than_two_active_citations_rejected(
    engine, persona_id, monkeypatch, cites_fn
):
    a, b, *_ = _hits(engine, persona_id, 4)
    retired = _lore(engine, persona_id, "hit", "old retired bit", retired=True)
    _agent(monkeypatch, [{"text": "Fixated on breakfast", "cites": cites_fn(a, b, retired)}])
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "no_op"
    assert _themes(engine, persona_id) == []
    assert outcome.details["rejections"] == [
        {"kind": "theme", "text": "Fixated on breakfast", "reason": "insufficient_citations"}
    ]


@pytest.mark.parametrize(
    "text, reason",
    [
        ("Keeps roasting Alice about her cats", "targets_member"),
        ("Keeps roasting <@U123> about cats", "targets_member"),
        ("You should always post about soup", "instruction"),
        ("Never mentions the moon", "instruction"),
        ("Hates the color orange intensely", "retired_readd"),
        ("Intensely hates the orange color", "retired_readd"),
    ],
)
def test_guardrail_rejections(engine, persona_id, monkeypatch, text, reason):
    a, b, *_ = _hits(engine, persona_id, 4)
    _lore(engine, persona_id, "hit", "Hates the color orange intensely", retired=True)
    _agent(monkeypatch, [{"text": text, "cites": [a, b]}])
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "no_op"
    assert _themes(engine, persona_id) == []
    assert outcome.details["rejections"] == [
        {"kind": "theme", "text": text, "reason": reason}
    ]


def test_rejections_do_not_block_valid_entries(engine, persona_id, monkeypatch):
    a, b, *_ = _hits(engine, persona_id, 4)
    _agent(
        monkeypatch,
        [
            {"text": "Tell everyone about soup", "cites": [a, b]},
            {"text": "Treats Mondays as a personal enemy", "cites": [a, b]},
        ],
    )
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "succeeded"
    assert [t.text for t in _themes(engine, persona_id)] == [
        "Treats Mondays as a personal enemy"
    ]
    assert [r["reason"] for r in outcome.details["rejections"]] == ["instruction"]


def test_initial_popularity_is_median_of_active_hits(engine, persona_id, monkeypatch):
    ids = [
        _lore(engine, persona_id, "hit", f"hit {p}", popularity=p) for p in (2, 9, 4, 20)
    ]
    # retired hits and non-hit kinds do not count toward the median
    _lore(engine, persona_id, "hit", "gone", popularity=1000, retired=True)
    _lore(engine, persona_id, "consolidated", "folded", popularity=500)
    _agent(monkeypatch, [{"text": "Fixated on breakfast", "cites": ids[:2]}])
    _run(engine, persona_id)
    (theme,) = _themes(engine, persona_id)
    assert theme.popularity_score == round((4 + 9) / 2)


def test_initial_popularity_zero_without_hits(engine, persona_id, monkeypatch):
    a = _lore(engine, persona_id, "consolidated", "folded one", popularity=50)
    b = _lore(engine, persona_id, "consolidated", "folded two", popularity=70)
    _agent(monkeypatch, [{"text": "Fixated on breakfast", "cites": [a, b]}])
    _run(engine, persona_id)
    (theme,) = _themes(engine, persona_id)
    assert theme.popularity_score == 0


def test_agent_receives_operator_retired_memory(engine, persona_id, monkeypatch):
    _hits(engine, persona_id, 2)
    _lore(engine, persona_id, "hit", "retired bit", retired=True)
    calls: list = []
    _agent(monkeypatch, [], calls)
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "no_op"
    assert [r["text"] for r in calls[0]["retired"]] == ["retired bit"]
    assert len(calls[0]["active"]) == 2


def test_reply_without_entries_list_fails_the_run(engine, persona_id, monkeypatch):
    monkeypatch.setattr(idea_generate, "_call_theme_agent", lambda *_: "no json here")
    with pytest.raises(ValueError):
        idea_generate._compute(BrainJobInput(persona_id=persona_id, job_kind="idea_generate"))
