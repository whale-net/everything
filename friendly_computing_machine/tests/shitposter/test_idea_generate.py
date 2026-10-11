"""Idea-generator job body: theme and novel branches, novel gate, pool targeting, run cap.

Runs _compute/_apply against an in-memory SQLite DB with the generator agents mocked.
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
    NovelGateReading,
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
    assert payload["novel"]["gate"]["open"] is False
    assert payload["novel"]["gate"]["reason"] == "no_engagement_in_window"
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
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "failed"
    assert "ValueError" in outcome.error
    assert _themes(engine, persona_id) == []


# --- novel branch ---


@pytest.fixture
def gate_env(monkeypatch):
    for name in (
        "FCM_SHITPOSTER_NOVEL_GATE_RECENT_DAYS",
        "FCM_SHITPOSTER_NOVEL_GATE_BASELINE_DAYS",
        "FCM_SHITPOSTER_NOVEL_GATE_FRACTION",
        "FCM_SHITPOSTER_NOVEL_GENERATOR_AGENT_ID",
    ):
        monkeypatch.delenv(name, raising=False)


_post_ids = iter(range(1000, 100000))


def _engagement(engine, persona_id, score, days_ago):
    with Session(engine) as s:
        s.add(
            ShitposterPostEngagement(
                post_id=next(_post_ids),
                persona_id=persona_id,
                finalized_at=datetime.datetime.now(datetime.timezone.utc)
                - datetime.timedelta(days=days_ago),
                distinct_reactors=score,
                reactions_by_emoji={},
                distinct_repliers=0,
                negative_reactions=0,
            )
        )
        s.commit()


def _open_gate(engine, persona_id):
    # recent mean 2, baseline mean (2+10)/2 = 6; 2 < 0.8 * 6
    _engagement(engine, persona_id, 2, days_ago=1)
    _engagement(engine, persona_id, 10, days_ago=20)


def _theme_at_target(engine, persona_id, hits=17):
    _hits(engine, persona_id, hits, popularity=0)
    for i in range(3):
        _lore(engine, persona_id, "theme", f"theme {i}")


def _novel_agent(monkeypatch, entries, calls=None):
    def fake(active, retired, max_entries):
        if calls is not None:
            calls.append({"active": active, "retired": retired, "max": max_entries})
        return json.dumps({"entries": entries})

    monkeypatch.setattr(idea_generate, "_call_novel_agent", fake)


def _no_novel_agent(monkeypatch):
    def boom(*_):
        raise AssertionError("novel agent must not be called")

    monkeypatch.setattr(idea_generate, "_call_novel_agent", boom)


def _novels(engine, persona_id):
    return [r for r in dal.active_lore(persona_id) if r.kind == "novel"]


def _gate(engine, persona_id):
    with Session(engine) as s:
        return idea_generate.read_novel_gate(
            s, persona_id, datetime.datetime.now(datetime.timezone.utc)
        )


@pytest.mark.parametrize(
    "fraction, is_open",
    [("0.8", False), ("0.81", True), ("0.79", False)],
    ids=["at_boundary", "just_above", "just_below"],
)
def test_gate_opens_only_below_fraction_of_baseline(
    engine, persona_id, gate_env, monkeypatch, fraction, is_open
):
    monkeypatch.setenv("FCM_SHITPOSTER_NOVEL_GATE_FRACTION", fraction)
    # recent mean 4, baseline mean (4+6)/2 = 5; threshold 0.8 * 5 = 4 exactly
    _engagement(engine, persona_id, 4, days_ago=1)
    _engagement(engine, persona_id, 6, days_ago=20)
    # outside the baseline window, ignored
    _engagement(engine, persona_id, 1000, days_ago=45)
    gate = _gate(engine, persona_id)
    assert gate.recent_mean == pytest.approx(4)
    assert gate.baseline_mean == pytest.approx(5)
    assert gate.fraction == pytest.approx(float(fraction))
    assert gate.open is is_open
    assert gate.reason == ("engagement_drop" if is_open else "no_engagement_drop")


def test_gate_closed_when_recent_window_empty(engine, persona_id, gate_env):
    _engagement(engine, persona_id, 10, days_ago=20)
    gate = _gate(engine, persona_id)
    assert gate.open is False
    assert gate.recent_mean is None and gate.baseline_mean == pytest.approx(10)
    assert gate.reason == "no_engagement_in_window"


def test_gate_closed_when_baseline_window_empty(
    engine, persona_id, gate_env, monkeypatch
):
    monkeypatch.setenv("FCM_SHITPOSTER_NOVEL_GATE_BASELINE_DAYS", "3")
    _engagement(engine, persona_id, 1, days_ago=5)
    gate = _gate(engine, persona_id)
    assert gate.open is False
    assert gate.recent_mean == pytest.approx(1) and gate.baseline_mean is None
    assert gate.reason == "no_engagement_in_window"


def test_gate_closed_with_no_engagement_at_all(engine, persona_id, gate_env):
    gate = _gate(engine, persona_id)
    assert (gate.open, gate.recent_mean, gate.baseline_mean) == (False, None, None)
    assert gate.reason == "no_engagement_in_window"


def test_closed_gate_never_calls_novel_agent(engine, persona_id, gate_env, monkeypatch):
    _theme_at_target(engine, persona_id)
    _engagement(engine, persona_id, 10, days_ago=1)
    _engagement(engine, persona_id, 10, days_ago=20)
    _no_agent(monkeypatch)
    _no_novel_agent(monkeypatch)
    payload, outcome = _run(engine, persona_id)
    assert outcome.status == "no_op"
    assert payload["novel"]["gate"]["open"] is False
    assert payload["novel"]["gate"]["reason"] == "no_engagement_drop"


def test_novel_entry_records_gate_reading_as_cause(
    engine, persona_id, gate_env, monkeypatch
):
    for p in (2, 9, 4, 20):
        _lore(engine, persona_id, "hit", f"hit {p}", popularity=p)
    _hits(engine, persona_id, 13, popularity=4)
    for i in range(3):
        _lore(engine, persona_id, "theme", f"theme {i}")
    _open_gate(engine, persona_id)
    _no_agent(monkeypatch)
    calls: list = []
    _novel_agent(monkeypatch, [{"text": "Has opinions about lighthouses"}], calls)

    payload, outcome = _run(engine, persona_id)

    assert len(calls) == 1
    assert outcome.status == "succeeded"
    gate = payload["novel"]["gate"]
    assert gate == {
        "open": True,
        "recent_mean": pytest.approx(2),
        "baseline_mean": pytest.approx(6),
        "fraction": pytest.approx(0.8),
        "reason": "engagement_drop",
    }
    assert payload["novel"]["share"] == 0.0 and payload["novel"]["target"] == 0.10
    (novel,) = _novels(engine, persona_id)
    assert novel.text == "Has opinions about lighthouses"
    assert novel.popularity_score == 4  # median of active hits
    entry = outcome.details["entries"][0]
    assert entry["lore_id"] == novel.id and entry["kind"] == "novel"
    assert entry["cited"] == [] and entry["agent_id"]
    assert outcome.details["novel"]["gate"]["open"] is True

    with Session(engine) as s:
        change = s.exec(
            select(ShitposterMemoryChange).where(
                ShitposterMemoryChange.entity_id == novel.id
            )
        ).one()
    assert (change.entity_kind, change.operation, change.cause_kind) == (
        "lore",
        "add",
        "novel_gate",
    )
    reading = NovelGateReading.from_cause_ref(change.cause_ref)
    assert reading == NovelGateReading(recent_mean=2.0, baseline_mean=6.0, fraction=0.8)
    assert json.loads(change.cause_ref) == {
        "recent_mean": 2.0,
        "baseline_mean": 6.0,
        "fraction": 0.8,
    }


def test_novel_share_at_target_writes_no_novel(engine, persona_id, gate_env, monkeypatch):
    # 2 novel of 20 active = 0.10, exactly at the novel target
    _theme_at_target(engine, persona_id, hits=15)
    for i in range(2):
        _lore(engine, persona_id, "novel", f"novel {i}")
    _open_gate(engine, persona_id)
    _no_agent(monkeypatch)
    _no_novel_agent(monkeypatch)
    payload, outcome = _run(engine, persona_id)
    assert outcome.status == "no_op"
    assert payload["novel"]["share"] == pytest.approx(0.10)
    assert payload["novel"]["gate"]["open"] is True
    assert len(_novels(engine, persona_id)) == 2


def test_novel_share_above_target_writes_no_novel(
    engine, persona_id, gate_env, monkeypatch
):
    _theme_at_target(engine, persona_id, hits=14)
    for i in range(3):
        _lore(engine, persona_id, "novel", f"novel {i}")
    _open_gate(engine, persona_id)
    _no_agent(monkeypatch)
    _no_novel_agent(monkeypatch)
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "no_op"
    assert len(_novels(engine, persona_id)) == 3


def test_novel_proposals_pass_guardrails(engine, persona_id, gate_env, monkeypatch):
    _theme_at_target(engine, persona_id)
    _open_gate(engine, persona_id)
    _no_agent(monkeypatch)
    _novel_agent(
        monkeypatch,
        [
            {"text": "Keeps roasting Alice about her cats"},
            {"text": "Collects receipts from imaginary shops"},
        ],
    )
    _, outcome = _run(engine, persona_id)
    assert [n.text for n in _novels(engine, persona_id)] == [
        "Collects receipts from imaginary shops"
    ]
    assert outcome.details["rejections"] == [
        {"kind": "novel", "text": "Keeps roasting Alice about her cats", "reason": "targets_member"}
    ]


def test_theme_and_novel_share_the_run_cap(engine, persona_id, gate_env, monkeypatch):
    a, b, *_ = _hits(engine, persona_id, 4)
    _open_gate(engine, persona_id)
    theme_calls: list = []
    novel_calls: list = []
    _agent(monkeypatch, [{"text": "Fixated on breakfast foods", "cites": [a, b]}], theme_calls)
    _novel_agent(
        monkeypatch,
        [{"text": "Wonders about lighthouses"}, {"text": "Narrates the weather badly"}],
        novel_calls,
    )
    _, outcome = _run(engine, persona_id)
    # cap 2: theme took one slot, so novel was asked for one
    assert theme_calls[0]["max"] == 2 and novel_calls[0]["max"] == 1
    assert [e["kind"] for e in outcome.details["entries"]] == ["theme", "novel"]
    assert len(_themes(engine, persona_id)) + len(_novels(engine, persona_id)) == 2
    assert outcome.details["rejections"] == [
        {"kind": "novel", "text": "Narrates the weather badly", "reason": "run_cap"}
    ]


def test_full_cap_from_theme_skips_novel(engine, persona_id, gate_env, monkeypatch):
    a, b, *_ = _hits(engine, persona_id, 4)
    _open_gate(engine, persona_id)
    _agent(
        monkeypatch,
        [
            {"text": "Fixated on breakfast foods", "cites": [a, b]},
            {"text": "Distrusts pigeons deeply", "cites": [a, b]},
        ],
    )
    _no_novel_agent(monkeypatch)
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "succeeded"
    assert len(_themes(engine, persona_id)) == 2
    assert _novels(engine, persona_id) == []


def test_failed_run_after_gate_keeps_gate_reading(
    engine, persona_id, gate_env, monkeypatch
):
    _theme_at_target(engine, persona_id)
    _open_gate(engine, persona_id)
    _no_agent(monkeypatch)

    def broken(*_):
        raise RuntimeError("agent unavailable")

    monkeypatch.setattr(idea_generate, "_call_novel_agent", broken)
    _, outcome = _run(engine, persona_id)
    assert outcome.status == "failed"
    assert "RuntimeError" in outcome.error
    gate = outcome.details["novel"]["gate"]
    assert gate["open"] is True and gate["reason"] == "engagement_drop"
    assert gate["recent_mean"] == pytest.approx(2)
    assert gate["baseline_mean"] == pytest.approx(6)
    assert gate["fraction"] == pytest.approx(0.8)
    assert outcome.details["entries"] == []
    assert _novels(engine, persona_id) == []
