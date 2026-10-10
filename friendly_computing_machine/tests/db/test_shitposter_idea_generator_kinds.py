"""Idea-generator schema kinds: CHECK migration round-trip, models, and runner timeouts.

SQLite cannot ALTER a CHECK constraint, so the migration's upgrade/downgrade DDL
is rendered offline against the Postgres dialect through alembic's real
Operations machinery. Row acceptance runs against SQLite tables built from the
models, whose constraint text must match the migration's upgraded text.

Red-proof: dropping 'novel' from the migration's new lore list turns the upgrade
DDL test red; dropping 'theme_synthesis' from the model constraint turns the
insert test red; removing the IDEA_GENERATE timeout turns the timeout test red.
"""

import datetime
import importlib.util
import io
import os
import re
from pathlib import Path
from types import ModuleType

import pytest
from alembic.operations import Operations
from alembic.runtime.migration import MigrationContext
from alembic.script import ScriptDirectory
from sqlalchemy import CheckConstraint, event
from sqlalchemy.dialects import postgresql
from sqlalchemy.exc import IntegrityError
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine

from friendly_computing_machine.src.friendly_computing_machine.models import (  # noqa: F401
    base,
    shitposter,
    shitposter_memory,
    slack,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobRun,
    ShitposterPersona,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterLoreKindEnum,
    ShitposterMemoryCauseKindEnum,
    ShitposterMemoryChange,
    is_generated_lore,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    BRAIN_JOB_TIMEOUTS,
)

REVISION = "d7a3e9c1b4f6"
_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"
T0 = datetime.datetime(2026, 10, 10, 12, 0, tzinfo=datetime.UTC)

# (constraint, table, values only before, values added)
_EXPECTED = {
    "ck_shitposterbrainjobrun_job_kind": (
        "shitposterbrainjobrun",
        {"harvest", "reflect", "write", "snapshot"},
        {"idea_generate"},
    ),
    "ck_shitposterloreentry_kind": (
        "shitposterloreentry",
        {"hit", "consolidated"},
        {"theme", "novel"},
    ),
    "ck_shitpostermemorychange_cause_kind": (
        "shitpostermemorychange",
        {"post_engagement", "suggestion", "operator", "fold"},
        {"theme_synthesis", "novel_gate"},
    ),
}


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


@pytest.fixture(scope="module")
def migration() -> ModuleType:
    matches = sorted(_versions_dir().glob(f"*{REVISION}*.py"))
    assert len(matches) == 1
    spec = importlib.util.spec_from_file_location("idea_generator_kinds", matches[0])
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    assert module.revision == REVISION
    return module


def _render(migration, func_name: str) -> str:
    buf = io.StringIO()
    ctx = MigrationContext.configure(
        dialect=postgresql.dialect(), opts={"as_sql": True, "output_buffer": buf}
    )
    with Operations.context(ctx):
        getattr(migration, func_name)()
    return buf.getvalue()


def _check_values(sql: str, constraint: str) -> set[str]:
    match = re.search(
        rf"ADD CONSTRAINT {constraint} CHECK \((.*?)\);", sql, flags=re.DOTALL
    )
    assert match, f"{constraint} not re-added in:\n{sql}"
    return set(re.findall(r"'([a-z_]+)'", match.group(1)))


def _values(condition: str) -> set[str]:
    return set(re.findall(r"'([a-z_]+)'", condition))


# --- migration ---------------------------------------------------------------


def test_revision_is_an_ancestor_of_the_single_head():
    script = ScriptDirectory(str(_versions_dir().parent))
    heads = script.get_heads()
    assert len(heads) == 1
    ancestors = {r.revision for r in script.iterate_revisions(heads[0], "base")}
    assert REVISION in ancestors


@pytest.mark.parametrize("func_name", ["upgrade", "downgrade"])
def test_migration_drops_each_constraint_on_fcm_schema(migration, func_name):
    sql = _render(migration, func_name)
    for constraint, (table, _old, _added) in _EXPECTED.items():
        assert f"ALTER TABLE fcm.{table} DROP CONSTRAINT {constraint};" in sql
        assert f"ALTER TABLE fcm.{table} ADD CONSTRAINT {constraint} CHECK" in sql


def test_upgrade_widens_each_constraint(migration):
    sql = _render(migration, "upgrade")
    for constraint, (_table, old, added) in _EXPECTED.items():
        assert _check_values(sql, constraint) == old | added


def test_downgrade_restores_the_original_lists(migration):
    sql = _render(migration, "downgrade")
    for constraint, (_table, old, _added) in _EXPECTED.items():
        assert _check_values(sql, constraint) == old


def test_old_lists_match_the_migrations_that_created_them(migration):
    sources = "".join(
        p.read_text()
        for p in _versions_dir().glob("*.py")
        if REVISION not in p.name
    )
    for _name, _table, old, _new in migration._CONSTRAINTS:
        assert f'"{old}"' in sources


def test_model_constraints_match_the_upgraded_migration(migration):
    tables = {t.name: t for t in Base.metadata.sorted_tables}
    for name, table, _old, new in migration._CONSTRAINTS:
        checks = {
            c.name: str(c.sqltext)
            for c in tables[table].constraints
            if isinstance(c, CheckConstraint)
        }
        assert checks[name] == new


# --- row acceptance ----------------------------------------------------------


@pytest.fixture
def engine():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")
        dbapi_conn.execute("PRAGMA foreign_keys=ON")

    Base.metadata.create_all(engine)
    return engine


@pytest.fixture
def persona_id(engine) -> int:
    with Session(engine) as s:
        row = ShitposterPersona(name="shitposter")
        s.add(row)
        s.commit()
        return row.id


def _lore(persona_id: int, kind: str) -> ShitposterLoreEntry:
    return ShitposterLoreEntry(
        persona_id=persona_id, text="t", kind=kind, valid_from=T0
    )


def _job(persona_id: int, kind: str) -> ShitposterBrainJobRun:
    return ShitposterBrainJobRun(
        persona_id=persona_id,
        job_kind=kind,
        trigger="schedule",
        status="succeeded",
        started_at=T0,
    )


def _change(persona_id: int, cause_kind: str) -> ShitposterMemoryChange:
    return ShitposterMemoryChange(
        changed_at=T0,
        persona_id=persona_id,
        entity_kind="lore",
        operation="add",
        after_text="t",
        cause_kind=cause_kind,
        cause_ref="ref",
    )


@pytest.mark.parametrize(
    "build,value",
    [
        (_lore, "theme"),
        (_lore, "novel"),
        (_job, "idea_generate"),
        (_change, "theme_synthesis"),
        (_change, "novel_gate"),
    ],
)
def test_new_values_are_accepted(engine, persona_id, build, value):
    with Session(engine) as s:
        s.add(build(persona_id, value))
        s.commit()


@pytest.mark.parametrize("build", [_lore, _job, _change])
def test_unknown_values_are_still_rejected(engine, persona_id, build):
    with Session(engine) as s:
        s.add(build(persona_id, "bogus"))
        with pytest.raises(IntegrityError):
            s.commit()


# --- enums and runner --------------------------------------------------------


def test_enums_carry_the_new_values():
    assert ShitposterBrainJobKind("idea_generate") is ShitposterBrainJobKind.IDEA_GENERATE
    assert {k.value for k in ShitposterLoreKindEnum} == {
        "hit", "consolidated", "theme", "novel",
    }
    assert {"theme_synthesis", "novel_gate"} <= {
        k.value for k in ShitposterMemoryCauseKindEnum
    }


@pytest.mark.parametrize(
    "kind,generated",
    [("theme", True), ("novel", True), ("hit", False), ("consolidated", False)],
)
def test_is_generated_lore(kind, generated):
    assert is_generated_lore(kind) is generated
    assert is_generated_lore(ShitposterLoreKindEnum(kind)) is generated


def test_every_brain_job_kind_has_a_timeout():
    assert set(BRAIN_JOB_TIMEOUTS) == set(ShitposterBrainJobKind)
    assert BRAIN_JOB_TIMEOUTS[ShitposterBrainJobKind.IDEA_GENERATE] == (
        BRAIN_JOB_TIMEOUTS[ShitposterBrainJobKind.REFLECT]
    )
