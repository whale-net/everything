"""Shitposter source weights: SCD2 migration round-trip, lazy seed, revisions,
history, and bounds/ceiling/start-weight config validation.

The migration runs through alembic's real Operations machinery against SQLite;
the head is derived from the script directory, never pinned.

Red-proof: dropping the partial unique index from upgrade() turns the schema
test red; skipping the close in revise_weight turns the revision tests red;
removing the ceiling check from load_source_weight_config turns the
over-ceiling fallback test red.
"""

import datetime
import importlib.util
import logging
import os
from pathlib import Path
from types import ModuleType

import pytest
from alembic.operations import Operations
from alembic.runtime.migration import MigrationContext
from alembic.script import ScriptDirectory
from sqlalchemy import event, inspect
from sqlalchemy.exc import IntegrityError
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine import (
    shitposter_config as cfg,
)
from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_weight_dal as weights,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobRun,
    ShitposterPersona,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterSourceWeight,
)

REVISION = "a4c7e2f9b1d3"
TABLE = "shitpostersourceweight"
_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"
T0 = datetime.datetime(2026, 10, 11, 12, 0, tzinfo=datetime.UTC)
_ENV = (
    "FCM_SHITPOSTER_THEME_TARGET_WEIGHT",
    "FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT",
    "FCM_SHITPOSTER_THEME_WEIGHT_MIN",
    "FCM_SHITPOSTER_THEME_WEIGHT_MAX",
    "FCM_SHITPOSTER_NOVEL_WEIGHT_MIN",
    "FCM_SHITPOSTER_NOVEL_WEIGHT_MAX",
    "FCM_SHITPOSTER_SOURCE_WEIGHT_CEILING",
)


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch):
    for name in _ENV:
        monkeypatch.delenv(name, raising=False)


def _versions_dir() -> Path:
    candidates = []
    runfiles = os.environ.get("TEST_SRCDIR")
    if runfiles:
        candidates.append(
            Path(runfiles, os.environ.get("TEST_WORKSPACE", "")) / _VERSIONS_REL
        )
    candidates.extend(
        parent / _VERSIONS_REL for parent in Path(__file__).resolve().parents
    )
    for candidate in candidates:
        if candidate.is_dir():
            return candidate
    raise RuntimeError(f"could not locate {_VERSIONS_REL}")


def _sqlite():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    return engine


# --- migration ---------------------------------------------------------------


@pytest.fixture(scope="module")
def migration() -> ModuleType:
    matches = sorted(_versions_dir().glob(f"*{REVISION}*.py"))
    assert len(matches) == 1
    spec = importlib.util.spec_from_file_location("source_weight_migration", matches[0])
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    assert module.revision == REVISION
    return module


@pytest.fixture
def pre_engine():
    engine = _sqlite()
    Base.metadata.create_all(
        engine,
        tables=[ShitposterPersona.__table__, ShitposterBrainJobRun.__table__],
    )
    return engine


def _run(migration, name, engine):
    with engine.begin() as conn:
        with Operations.context(MigrationContext.configure(conn)):
            getattr(migration, name)()


def test_revision_is_an_ancestor_of_the_single_head():
    script = ScriptDirectory(str(_versions_dir().parent))
    heads = script.get_heads()
    assert len(heads) == 1
    ancestors = {r.revision for r in script.iterate_revisions(heads[0], "base")}
    assert REVISION in ancestors


def test_upgrade_creates_table_columns_and_current_index(migration, pre_engine):
    _run(migration, "upgrade", pre_engine)
    inspector = inspect(pre_engine)
    assert TABLE in inspector.get_table_names(schema="fcm")
    cols = {c["name"] for c in inspector.get_columns(TABLE, schema="fcm")}
    assert {
        "persona_id", "source", "weight", "cause", "brain_job_run_id",
        "valid_from", "valid_to",
    } <= cols
    indexes = {i["name"]: i for i in inspector.get_indexes(TABLE, schema="fcm")}
    current = indexes["uq_shitpostersourceweight_current"]
    assert current["unique"]
    assert current["column_names"] == ["persona_id", "source"]


def test_migrated_table_enforces_source_check_and_one_current_row(migration, pre_engine):
    _run(migration, "upgrade", pre_engine)
    with pre_engine.begin() as conn:
        conn.exec_driver_sql("INSERT INTO fcm.shitposterpersona (id, name) VALUES (1, 'p')")
    insert = (
        "INSERT INTO fcm.shitpostersourceweight (persona_id, source, weight, cause, "
        "valid_from, valid_to) VALUES (1, ?, 0.15, '{}', '2026-10-11', ?)"
    )
    with pre_engine.begin() as conn:
        conn.exec_driver_sql(insert, ("theme", "2026-10-12"))
        conn.exec_driver_sql(insert, ("theme", None))
    with pytest.raises(IntegrityError):
        with pre_engine.begin() as conn:
            conn.exec_driver_sql(insert, ("theme", None))
    with pytest.raises(IntegrityError):
        with pre_engine.begin() as conn:
            conn.exec_driver_sql(insert, ("hit", None))


def test_downgrade_removes_the_table(migration, pre_engine):
    _run(migration, "upgrade", pre_engine)
    _run(migration, "downgrade", pre_engine)
    assert TABLE not in inspect(pre_engine).get_table_names(schema="fcm")


# --- DAL ---------------------------------------------------------------------


@pytest.fixture
def engine(monkeypatch):
    engine = _sqlite()
    Base.metadata.create_all(
        engine,
        tables=[
            ShitposterPersona.__table__,
            ShitposterBrainJobRun.__table__,
            ShitposterSourceWeight.__table__,
        ],
    )
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    return engine


@pytest.fixture
def persona_id(engine) -> int:
    with Session(engine) as s:
        row = ShitposterPersona(name="shitposter")
        s.add(row)
        s.commit()
        return row.id


def _all_rows(engine, persona_id, source=None):
    with Session(engine) as s:
        stmt = select(ShitposterSourceWeight).where(
            ShitposterSourceWeight.persona_id == persona_id
        )
        if source:
            stmt = stmt.where(ShitposterSourceWeight.source == source)
        return list(s.exec(stmt.order_by(ShitposterSourceWeight.id)).all())


def test_first_read_seeds_default_start_weights(engine, persona_id):
    current = weights.get_current_weights(persona_id)

    assert set(current) == {"theme", "novel"}
    assert float(current["theme"].weight) == pytest.approx(0.15)
    assert float(current["novel"].weight) == pytest.approx(0.10)
    for row in current.values():
        assert row.valid_to is None
        assert row.cause["kind"] == weights.CAUSE_INITIAL
        assert row.brain_job_run_id is None
    assert len(_all_rows(engine, persona_id)) == 2


def test_second_read_does_not_reseed(engine, persona_id):
    first = weights.get_current_weights(persona_id)
    second = weights.get_current_weights(persona_id)

    assert {k: r.id for k, r in first.items()} == {k: r.id for k, r in second.items()}
    assert len(_all_rows(engine, persona_id)) == 2


def test_seed_uses_configured_start_weights(engine, persona_id, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_TARGET_WEIGHT", "0.2")
    monkeypatch.setenv("FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT", "0.12")

    current = weights.get_current_weights(persona_id)

    assert float(current["theme"].weight) == pytest.approx(0.2)
    assert float(current["novel"].weight) == pytest.approx(0.12)


def test_fallback_seed_records_fallback_cause(engine, persona_id, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_TARGET_WEIGHT", "0.5")

    current = weights.get_current_weights(persona_id)

    assert float(current["theme"].weight) == pytest.approx(0.15)
    assert float(current["novel"].weight) == pytest.approx(0.10)
    assert {r.cause["kind"] for r in current.values()} == {weights.CAUSE_FALLBACK}


def test_seed_is_per_persona(engine, persona_id):
    with Session(engine) as s:
        other = ShitposterPersona(name="other")
        s.add(other)
        s.commit()
        other_id = other.id
    weights.get_current_weights(persona_id)

    assert _all_rows(engine, other_id) == []
    weights.get_current_weights(other_id)
    assert len(_all_rows(engine, other_id)) == 2


def test_revise_closes_current_and_opens_successor(engine, persona_id):
    seeded = weights.get_current_weights(persona_id)
    with Session(engine) as s:
        run = ShitposterBrainJobRun(
            persona_id=persona_id, job_kind="idea_generate", trigger="schedule",
            status="running",
        )
        s.add(run)
        s.flush()
        cause = {"with_mean": 5.0, "without_mean": 3.0}
        new = weights.revise_weight(
            s, persona_id, "theme", 0.16, cause, run_id=run.id, now=T0
        )
        s.commit()
        new_id, run_id = new.id, run.id

    theme_rows = _all_rows(engine, persona_id, "theme")
    assert [r.id for r in theme_rows] == [seeded["theme"].id, new_id]
    closed, current = theme_rows
    assert closed.valid_to is not None
    assert current.valid_to is None
    assert float(current.weight) == pytest.approx(0.16)
    assert current.cause == {"with_mean": 5.0, "without_mean": 3.0}
    assert current.brain_job_run_id == run_id
    # the other source is untouched
    (novel,) = _all_rows(engine, persona_id, "novel")
    assert novel.valid_to is None and novel.id == seeded["novel"].id

    after = weights.get_current_weights(persona_id)
    assert after["theme"].id == new_id
    assert float(after["theme"].weight) == pytest.approx(0.16)


def test_repeated_revisions_keep_exactly_one_current_row_per_source(engine, persona_id):
    weights.get_current_weights(persona_id)
    with Session(engine) as s:
        for i, w in enumerate((0.16, 0.17, 0.16)):
            weights.revise_weight(
                s, persona_id, "theme", w, {"step": i},
                now=T0 + datetime.timedelta(days=i),
            )
        weights.revise_weight(s, persona_id, "novel", 0.11, {"step": 0}, now=T0)
        s.commit()

    for source, total in (("theme", 4), ("novel", 2)):
        rows = _all_rows(engine, persona_id, source)
        assert len(rows) == total
        assert sum(r.valid_to is None for r in rows) == 1


def test_revise_rejects_empty_cause_and_unknown_source(engine, persona_id):
    weights.get_current_weights(persona_id)
    with Session(engine) as s:
        with pytest.raises(ValueError):
            weights.revise_weight(s, persona_id, "theme", 0.2, {})
        with pytest.raises(ValueError):
            weights.revise_weight(s, persona_id, "hit", 0.2, {"x": 1})
    assert len(_all_rows(engine, persona_id, "theme")) == 1


def test_history_is_oldest_first_and_per_persona(engine, persona_id):
    seeded = weights.get_current_weights(persona_id)
    with Session(engine) as s:
        later = datetime.datetime.now(datetime.UTC) + datetime.timedelta(days=1)
        revised = weights.revise_weight(s, persona_id, "theme", 0.16, {"s": 1}, now=later)
        s.commit()
        revised_id = revised.id

    history = weights.list_weight_history(persona_id)
    assert len(history) == 3
    assert history[-1].id == revised_id
    assert {h.id for h in history[:2]} == {r.id for r in seeded.values()}
    assert weights.list_weight_history(persona_id + 999) == []


# --- config ------------------------------------------------------------------


def test_config_defaults():
    c = cfg.load_source_weight_config()
    assert (c.theme_start, c.novel_start) == (0.15, 0.10)
    assert (c.theme_min, c.theme_max, c.novel_min, c.novel_max) == (0.05, 0.30, 0.05, 0.30)
    assert c.ceiling == 0.40
    assert not c.fell_back


def test_config_valid_overrides(monkeypatch, caplog):
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_TARGET_WEIGHT", "0.25")
    monkeypatch.setenv("FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT", "0.2")
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_WEIGHT_MAX", "0.35")
    monkeypatch.setenv("FCM_SHITPOSTER_SOURCE_WEIGHT_CEILING", "0.5")
    with caplog.at_level(logging.WARNING):
        c = cfg.load_source_weight_config()
    assert (c.theme_start, c.novel_start, c.theme_max, c.ceiling) == (0.25, 0.2, 0.35, 0.5)
    assert not c.fell_back
    assert not [r for r in caplog.records if r.levelno >= logging.WARNING]


@pytest.mark.parametrize(
    "env",
    [
        {"FCM_SHITPOSTER_THEME_TARGET_WEIGHT": "0.31"},  # above theme max
        {"FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT": "0.04"},  # below novel min
        {  # each within bounds, sum above ceiling
            "FCM_SHITPOSTER_THEME_TARGET_WEIGHT": "0.25",
            "FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT": "0.2",
        },
    ],
)
def test_invalid_start_weights_fall_back_with_warning(monkeypatch, caplog, env):
    for name, value in env.items():
        monkeypatch.setenv(name, value)
    with caplog.at_level(logging.WARNING):
        c = cfg.load_source_weight_config()
    assert (c.theme_start, c.novel_start) == (0.15, 0.10)
    assert c.fell_back
    assert any(
        r.levelno == logging.WARNING and "starting source weights" in r.getMessage()
        for r in caplog.records
    )


def test_inconsistent_bounds_fall_back_to_default_bounds(monkeypatch, caplog):
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_WEIGHT_MIN", "0.3")
    monkeypatch.setenv("FCM_SHITPOSTER_THEME_WEIGHT_MAX", "0.2")
    with caplog.at_level(logging.WARNING):
        c = cfg.load_source_weight_config()
    assert (c.theme_min, c.theme_max, c.ceiling) == (0.05, 0.30, 0.40)
    assert any(r.levelno == logging.WARNING for r in caplog.records)
