"""Adaptive source weights: compute decisions (write-free) and apply revisions.

Runs against an in-memory SQLite DB; the idea-generator agents are mocked.

Red-proof. Each mutation was applied, run, and reverted:

  * step direction inverted in _proposal            -> outperform/underperform tests
  * per-group minimum check removed                 -> below-minimum tests
  * days_compared uses every day, not the overlap   -> overlapping-days test
  * bound clamp dropped                             -> bounds tests
  * ceiling check removed                           -> ceiling and tie-break tests
  * tie-break sorted by room ascending              -> tie-break test
  * failed-run apply writes weight decisions        -> failed-run test
"""

import datetime
import os

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
    shitposter_operator as operator,
)
from friendly_computing_machine.src.friendly_computing_machine.db import (  # noqa: E402
    util as db_util,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (  # noqa: E402
    shitposter_weight_dal,
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
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (  # noqa: E402
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (  # noqa: E402
    ShitposterLoreEntry,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
    ShitposterSourceWeight,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (  # noqa: E402
    SlackChannel,
    SlackMessage,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (  # noqa: E402
    SourceWeightConfig,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (  # noqa: E402
    idea_generate,
    source_weights,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (  # noqa: E402
    BrainJobInput,
)

RUN_ID = 1
ADMIN = "U_ADMIN"
ADMINS = frozenset({ADMIN})
TABLES = [
    SlackUser.__table__,
    SlackChannel.__table__,
    SlackMessage.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
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
NOW = datetime.datetime.now(datetime.timezone.utc)
CONFIG = SourceWeightConfig(
    theme_start=0.15,
    novel_start=0.10,
    theme_min=0.05,
    theme_max=0.30,
    novel_min=0.05,
    novel_max=0.30,
    ceiling=0.40,
)


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
    for name in list(os.environ):
        if name.startswith("FCM_SHITPOSTER_"):
            monkeypatch.delenv(name, raising=False)
    monkeypatch.setattr(idea_generate, "_member_names", lambda: ["Alice"])
    return engine


@pytest.fixture
def persona_id(engine) -> int:
    with Session(engine) as s:
        persona = ShitposterPersona(name="shitposter")
        s.add(persona)
        s.commit()
        pid = persona.id
    shitposter_weight_dal.get_current_weights(pid)  # seed 15% / 10%
    return pid


_versions = iter(range(1, 1_000_000))
_ts = iter(range(1, 1_000_000))


def _lore(s, persona_id, kind) -> int:
    row = ShitposterLoreEntry(persona_id=persona_id, text=f"{kind} lore", kind=kind)
    s.add(row)
    s.flush()
    return row.id


def _snapshot(engine, persona_id, kinds) -> int:
    """A context snapshot holding one lore row of each kind in `kinds`."""
    with Session(engine) as s:
        snap = ShitposterContextSnapshot(
            persona_id=persona_id,
            version=next(_versions),
            brain_job_run_id=RUN_ID,
            token_count=0,
            rendered_text="",
        )
        s.add(snap)
        s.flush()
        for rank, kind in enumerate(kinds):
            s.add(
                ShitposterSnapshotItem(
                    snapshot_id=snap.id,
                    item_kind="lore",
                    item_id=_lore(s, persona_id, kind),
                    rank=rank,
                )
            )
        s.commit()
        return snap.id


def _posts(engine, persona_id, snapshot_id, score, days_ago, n=1, finalized=True):
    """`n` posts written `days_ago` from `snapshot_id`, each with popularity `score`."""
    with Session(engine) as s:
        for _ in range(n):
            post = ShitposterPost(
                slack_channel_id=1,
                slack_message_ts=f"{next(_ts)}.0",
                persona_id=persona_id,
                persona_revision_id=1,
                trigger="scheduled",
                principal_iss="iss",
                principal_sub="sub",
                principal_kind="service",
                whagent_session_id="sess",
                created_at=NOW - datetime.timedelta(days=days_ago, hours=1),
                context_snapshot_id=snapshot_id,
            )
            s.add(post)
            s.flush()
            if finalized:
                s.add(
                    ShitposterPostEngagement(
                        post_id=post.id,
                        persona_id=persona_id,
                        finalized_at=NOW - datetime.timedelta(days=max(days_ago - 3, 0)),
                        distinct_reactors=score,
                        reactions_by_emoji={},
                        distinct_repliers=0,
                        negative_reactions=0,
                    )
                )
        s.commit()


def _theme_vs_plain(engine, persona_id, with_score, without_score, days=3, per_day=4):
    """`per_day` posts with theme lore and as many without, on each of `days` days."""
    with_snap = _snapshot(engine, persona_id, ["theme", "hit"])
    without_snap = _snapshot(engine, persona_id, ["hit"])
    for day in range(1, days + 1):
        _posts(engine, persona_id, with_snap, with_score, day, n=per_day)
        _posts(engine, persona_id, without_snap, without_score, day, n=per_day)


def _decisions(engine, persona_id):
    with Session(engine) as s:
        return {d.source: d for d in source_weights.compute_weight_changes(s, persona_id, NOW)}


def _apply(engine, persona_id, decisions):
    with Session(engine) as s:
        details = source_weights.apply_weight_changes(
            s, persona_id, RUN_ID, [d.as_payload() for d in decisions.values()]
        )
        s.commit()
    return details


def _current(persona_id) -> dict[str, float]:
    rows = shitposter_weight_dal.get_current_weights(persona_id)
    return {k: float(v.weight) for k, v in rows.items()}


def _cmp(source, with_mean, without_mean, n=10):
    return source_weights.SourceComparison(
        source=source,
        with_mean=with_mean,
        without_mean=without_mean,
        with_n=n,
        without_n=n,
        days_compared=3,
    )


def _resolve(theme, novel, current, config=CONFIG, step=0.01, min_posts=10):
    decisions = source_weights.resolve_steps(
        {"theme": theme, "novel": novel}, current, config, step, min_posts
    )
    return {d.source: d for d in decisions}


RISE = (5.0, 1.0)
FALL = (1.0, 5.0)


# --- step direction ----------------------------------------------------------


def test_outperform_steps_up_underperform_steps_down(engine, persona_id, monkeypatch):
    # theme lore outperforms; the whole run steps theme up and targets the new weight
    _theme_vs_plain(engine, persona_id, with_score=10, without_score=2)
    for i in range(20):  # keep theme below target so the theme agent runs
        with Session(engine) as s:
            _lore(s, persona_id, "hit")
            s.commit()
    monkeypatch.setattr(
        idea_generate, "_call_theme_agent", lambda *a: '{"entries": []}'
    )
    payload = idea_generate._compute(
        BrainJobInput(persona_id=persona_id, job_kind="idea_generate")
    )
    assert payload["theme"]["target"] == pytest.approx(0.16)
    with Session(engine) as s:
        outcome = idea_generate._apply(s, RUN_ID, payload)
        s.commit()

    assert outcome.status == "succeeded"
    assert outcome.details["weight_changes"] == [
        {"source": "theme", "old": 0.15, "new": 0.16, "with_mean": 10.0, "without_mean": 2.0},
        {"source": "novel", "unchanged_reason": "below_minimum"},
    ]
    assert _current(persona_id) == {"theme": pytest.approx(0.16), "novel": pytest.approx(0.10)}
    theme_row = shitposter_weight_dal.get_current_weights(persona_id)["theme"]
    assert theme_row.brain_job_run_id == RUN_ID
    assert theme_row.cause == {
        "with_mean": 10.0,
        "without_mean": 2.0,
        "with_n": 12,
        "without_n": 12,
        "days_compared": 3,
    }


def test_underperform_steps_down(engine, persona_id):
    _theme_vs_plain(engine, persona_id, with_score=1, without_score=6)
    decisions = _decisions(engine, persona_id)
    assert decisions["theme"].new == pytest.approx(0.14)
    # compute is write-free
    assert _current(persona_id)["theme"] == pytest.approx(0.15)

    details = _apply(engine, persona_id, decisions)
    assert details[0] == {
        "source": "theme", "old": 0.15, "new": 0.14, "with_mean": 1.0, "without_mean": 6.0
    }
    assert _current(persona_id)["theme"] == pytest.approx(0.14)


def test_novel_source_compared_independently(engine, persona_id):
    with_snap = _snapshot(engine, persona_id, ["novel"])
    without_snap = _snapshot(engine, persona_id, ["theme"])
    for day in (1, 2):
        _posts(engine, persona_id, with_snap, 8, day, n=5)
        _posts(engine, persona_id, without_snap, 3, day, n=5)
    decisions = _decisions(engine, persona_id)
    assert decisions["novel"].new == pytest.approx(0.11)
    # the same posts are the theme's "with" group, and it underperforms
    assert decisions["theme"].new == pytest.approx(0.14)


def test_equal_means_leave_weight_unchanged(engine, persona_id):
    _theme_vs_plain(engine, persona_id, with_score=4, without_score=4)
    assert _decisions(engine, persona_id)["theme"].unchanged_reason == "equal_means"


# --- per-group minimum -------------------------------------------------------


def test_below_minimum_leaves_weight_unchanged_with_reason(engine, persona_id):
    # 9 posts with theme lore against 12 without: below the default minimum of 10
    with_snap = _snapshot(engine, persona_id, ["theme"])
    without_snap = _snapshot(engine, persona_id, ["hit"])
    for day, n_with in ((1, 3), (2, 3), (3, 3)):
        _posts(engine, persona_id, with_snap, 50, day, n=n_with)
        _posts(engine, persona_id, without_snap, 1, day, n=4)
    decisions = _decisions(engine, persona_id)
    assert decisions["theme"].unchanged_reason == "below_minimum"
    assert decisions["theme"].comparison.with_n == 9

    details = _apply(engine, persona_id, decisions)
    assert {"source": "theme", "unchanged_reason": "below_minimum"} in details
    assert _current(persona_id)["theme"] == pytest.approx(0.15)
    assert len(shitposter_weight_dal.list_weight_history(persona_id)) == 2


def test_minimum_is_configurable(engine, persona_id, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_WEIGHT_MIN_POSTS", "3")
    _theme_vs_plain(engine, persona_id, with_score=9, without_score=1, days=1, per_day=3)
    assert _decisions(engine, persona_id)["theme"].new == pytest.approx(0.16)


# --- overlapping days --------------------------------------------------------


def test_only_overlapping_days_are_compared(engine, persona_id):
    with_snap = _snapshot(engine, persona_id, ["theme"])
    without_snap = _snapshot(engine, persona_id, ["hit"])
    # day 1: both kinds (the only day compared)
    _posts(engine, persona_id, with_snap, 4, 1, n=10)
    _posts(engine, persona_id, without_snap, 6, 1, n=10)
    # day 2: only with-theme posts, very popular; day 3: only without, very popular
    _posts(engine, persona_id, with_snap, 100, 2, n=10)
    _posts(engine, persona_id, without_snap, 100, 3, n=10)
    # outside the 30-day window, and unfinalized posts, are ignored
    _posts(engine, persona_id, with_snap, 100, 40, n=10)
    _posts(engine, persona_id, without_snap, 0, 40, n=10)
    _posts(engine, persona_id, with_snap, 100, 1, n=10, finalized=False)

    cmp = _decisions(engine, persona_id)["theme"].comparison
    assert cmp.days_compared == 1
    assert (cmp.with_n, cmp.without_n) == (10, 10)
    assert (cmp.with_mean, cmp.without_mean) == (pytest.approx(4), pytest.approx(6))
    assert _decisions(engine, persona_id)["theme"].new == pytest.approx(0.14)


def test_window_is_configurable(engine, persona_id, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_WEIGHT_WINDOW_DAYS", "3")
    # posts sit an hour past their day mark, so a 3-day window holds days 1 and 2
    _theme_vs_plain(engine, persona_id, with_score=9, without_score=1, days=5, per_day=4)
    cmp = _decisions(engine, persona_id)["theme"].comparison
    assert cmp.days_compared == 2 and cmp.with_n == 8


# --- bounds ------------------------------------------------------------------


def test_bounds_clamp():
    out = _resolve(_cmp("theme", *RISE), _cmp("novel", *FALL), {"theme": 0.295, "novel": 0.055})
    assert out["theme"].new == pytest.approx(0.30)
    assert out["novel"].new == pytest.approx(0.05)


def test_weight_at_bound_stays_with_reason():
    out = _resolve(_cmp("theme", *RISE), _cmp("novel", *FALL), {"theme": 0.30, "novel": 0.05})
    assert out["theme"].unchanged_reason == "bound"
    assert out["novel"].unchanged_reason == "bound"


def test_step_is_configurable(engine, persona_id, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_WEIGHT_STEP", "0.03")
    _theme_vs_plain(engine, persona_id, with_score=9, without_score=1)
    assert _decisions(engine, persona_id)["theme"].new == pytest.approx(0.18)


# --- ceiling -----------------------------------------------------------------


def test_ceiling_breach_not_applied():
    below = _cmp("novel", 5.0, 1.0, n=3)  # novel stays put: below minimum
    out = _resolve(_cmp("theme", *RISE), below, {"theme": 0.25, "novel": 0.15})
    assert out["theme"].unchanged_reason == "ceiling"
    assert out["theme"].new is None
    assert out["novel"].unchanged_reason == "below_minimum"


def test_falling_step_still_applies_at_ceiling():
    out = _resolve(_cmp("theme", *FALL), _cmp("novel", *RISE), {"theme": 0.25, "novel": 0.15})
    assert out["theme"].new == pytest.approx(0.24)
    assert out["novel"].new == pytest.approx(0.16)


def test_ceiling_breach_not_applied_end_to_end(engine, persona_id, monkeypatch):
    with Session(engine) as s:
        shitposter_weight_dal.revise_weight(s, persona_id, "theme", 0.30, {"test": True})
        s.commit()
    config = SourceWeightConfig(**{**CONFIG.__dict__, "theme_max": 0.35})
    monkeypatch.setattr(source_weights, "load_source_weight_config", lambda: config)
    _theme_vs_plain(engine, persona_id, with_score=9, without_score=1)
    decisions = _decisions(engine, persona_id)
    assert decisions["theme"].unchanged_reason == "ceiling"
    details = _apply(engine, persona_id, decisions)
    assert {"source": "theme", "unchanged_reason": "ceiling"} in details
    assert _current(persona_id)["theme"] == pytest.approx(0.30)


@pytest.mark.parametrize(
    "novel_max, stepper, held",
    [(0.30, "novel", "theme"), (0.25, "theme", "novel")],
    ids=["novel_further_below_its_max", "theme_further_below_its_max"],
)
def test_tie_break_when_both_rise(novel_max, stepper, held):
    config = SourceWeightConfig(**{**CONFIG.__dict__, "novel_max": novel_max})
    # both stepping gives 0.405 > 0.40; either one alone gives 0.395
    current = {"theme": 0.20, "novel": 0.185}
    out = _resolve(_cmp("theme", *RISE), _cmp("novel", *RISE), current, config=config)
    assert out[stepper].new == pytest.approx(current[stepper] + 0.01)
    assert out[held].unchanged_reason == "ceiling"


def test_both_rise_and_both_fit():
    out = _resolve(_cmp("theme", *RISE), _cmp("novel", *RISE), {"theme": 0.15, "novel": 0.10})
    assert out["theme"].new == pytest.approx(0.16)
    assert out["novel"].new == pytest.approx(0.11)


# --- failed run --------------------------------------------------------------


def test_failed_run_writes_no_weight_revision(engine, persona_id, monkeypatch):
    _theme_vs_plain(engine, persona_id, with_score=10, without_score=2)
    for _ in range(20):
        with Session(engine) as s:
            _lore(s, persona_id, "hit")
            s.commit()

    def broken(*_):
        raise TimeoutError("agent timed out")

    monkeypatch.setattr(idea_generate, "_call_theme_agent", broken)
    payload = idea_generate._compute(
        BrainJobInput(persona_id=persona_id, job_kind="idea_generate")
    )
    # the data would step theme up, but the run fails
    assert [d["new"] for d in payload["weight_decisions"] if d["source"] == "theme"] == [
        pytest.approx(0.16)
    ]
    with Session(engine) as s:
        outcome = idea_generate._apply(s, RUN_ID, payload)
        s.commit()

    assert outcome.status == "failed"
    assert "TimeoutError" in outcome.error
    assert _current(persona_id) == {"theme": pytest.approx(0.15), "novel": pytest.approx(0.10)}
    assert len(shitposter_weight_dal.list_weight_history(persona_id)) == 2


def test_apply_skips_a_weight_moved_since_compute(engine, persona_id):
    _theme_vs_plain(engine, persona_id, with_score=10, without_score=2)
    decisions = _decisions(engine, persona_id)
    with Session(engine) as s:
        shitposter_weight_dal.revise_weight(s, persona_id, "theme", 0.20, {"test": True})
        s.commit()
    details = _apply(engine, persona_id, decisions)
    assert {"source": "theme", "unchanged_reason": "superseded"} in details
    assert _current(persona_id)["theme"] == pytest.approx(0.20)


# --- operator history --------------------------------------------------------


def test_weight_history_visible_to_operator(engine, persona_id):
    _theme_vs_plain(engine, persona_id, with_score=10, without_score=2)
    details = _apply(engine, persona_id, _decisions(engine, persona_id))

    text = operator.handle_operator_command("weights", ADMIN, ADMINS)
    lines = text.splitlines()
    assert lines[0] == "Weight revisions 1-3 of 3, newest first."
    assert "theme 16%" in lines[1] and "(current)" in lines[1] and f"run #{RUN_ID}" in lines[1]
    assert lines[2] == (
        "  cause: with lore mean 10.0 (n 12) vs without mean 2.0 (n 12) over 3 days"
    )
    assert "theme 15%" in text and "until " in text
    assert "novel 10%" in text
    assert operator.handle_operator_command("weights", "U_OTHER", ADMINS) == operator.NOT_ADMIN

    # the run listing renders the run's weight_changes
    with Session(engine) as s:
        run = ShitposterBrainJobRun(
            persona_id=persona_id,
            job_kind=ShitposterBrainJobKind.IDEA_GENERATE.value,
            trigger=ShitposterBrainJobTrigger.SCHEDULE.value,
            status=ShitposterBrainJobStatus.SUCCEEDED.value,
            started_at=NOW,
            details={"entries": [], "weight_changes": details},
        )
        s.add(run)
        s.commit()
    runs = operator.handle_operator_command("ideas", ADMIN, ADMINS)
    assert "stepped 1 weight" in runs
    assert "source theme, old 0.15, new 0.16, with_mean 10.0, without_mean 2.0" in runs
    assert "source novel, unchanged_reason below_minimum" in runs


def test_cli_weights_lists_history(engine, persona_id):
    from typer.testing import CliRunner

    from friendly_computing_machine.src.friendly_computing_machine.cli.tools_cli import app

    with Session(engine) as s:
        shitposter_weight_dal.revise_weight(
            s,
            persona_id,
            "novel",
            0.11,
            _cmp("novel", 3.0, 1.0).as_cause() | {"without_n": 11, "days_compared": 2},
        )
        s.commit()
    result = CliRunner().invoke(app, ["shitposter", "weights", "--page-size", "1"])
    assert result.exit_code == 0, result.output
    assert "Weight revisions 1-1 of 3, newest first." in result.output
    assert "novel 11%" in result.output
    assert "with lore mean 3.0 (n 10) vs without mean 1.0 (n 11) over 2 days" in result.output
