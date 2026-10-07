"""Shitposter memory: migration round-trip and the SCD2 DAL with mandatory cause.

The DAL runs against SQLite with the memory migration applied through alembic's
real Operations machinery. The head is derived from the script directory.

Red-proof: removing the cause check in _require_cause, dropping the valid_to
close in _supersede_attribute, or dropping the operator flag in operator_retire
turns the matching test red.
"""

import datetime
import importlib.util
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

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models import (  # noqa: F401
    base,
    shitposter,
    shitposter_memory,
    slack,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterPersona,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
)

MEMORY_REVISION = "e5b0d3a8c2f5"
MEMORY_TABLES = frozenset(
    {"shitposterpersonaattribute", "shitposterloreentry", "shitpostermemorychange"}
)
_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"

T0 = datetime.datetime(2026, 10, 1, 12, 0, tzinfo=datetime.UTC)
T1 = T0 + datetime.timedelta(hours=1)
T2 = T0 + datetime.timedelta(hours=2)
T3 = T0 + datetime.timedelta(hours=3)


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


def _load(path: Path) -> ModuleType:
    spec = importlib.util.spec_from_file_location(f"mig_{path.stem}", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture(scope="module")
def migration() -> ModuleType:
    matches = sorted(_versions_dir().glob(f"*{MEMORY_REVISION}*.py"))
    assert len(matches) == 1
    module = _load(matches[0])
    assert module.revision == MEMORY_REVISION
    return module


def _run(migration, func_name: str, engine) -> None:
    with engine.begin() as conn:
        with Operations.context(MigrationContext.configure(conn)):
            getattr(migration, func_name)()


@pytest.fixture
def pre_engine():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")
        dbapi_conn.create_function(
            "NOW", 0, lambda: datetime.datetime.now(datetime.UTC).isoformat(" ")
        )

    Base.metadata.create_all(
        engine,
        tables=[t for t in Base.metadata.sorted_tables if t.name not in MEMORY_TABLES],
    )
    return engine


@pytest.fixture
def engine(pre_engine, migration):
    _run(migration, "upgrade", pre_engine)
    return pre_engine


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


def _naive(ts: datetime.datetime) -> datetime.datetime:
    # SQLite hands back naive datetimes; Postgres returns aware ones.
    return ts.replace(tzinfo=None)


def _attr_rows(session, persona_id: int, key: str):
    return list(
        session.exec(
            select(ShitposterPersonaAttribute)
            .where(ShitposterPersonaAttribute.persona_id == persona_id)
            .where(ShitposterPersonaAttribute.attribute_key == key)
            .order_by(ShitposterPersonaAttribute.id)
        ).all()
    )


# --- migration ---------------------------------------------------------------


def test_memory_revision_is_the_chain_head():
    script = ScriptDirectory(str(_versions_dir().parent))
    heads = script.get_heads()
    assert len(heads) == 1
    assert heads[0] == MEMORY_REVISION


def test_upgrade_creates_memory_tables_and_current_indexes(migration, pre_engine):
    _run(migration, "upgrade", pre_engine)
    inspector = inspect(pre_engine)
    assert MEMORY_TABLES <= set(inspector.get_table_names(schema="fcm"))

    attr_cols = {c["name"] for c in inspector.get_columns("shitposterpersonaattribute", schema="fcm")}
    assert {
        "persona_id", "attribute_key", "text", "status",
        "retired_by_operator", "valid_from", "valid_to",
    } <= attr_cols
    change_cols = {c["name"] for c in inspector.get_columns("shitpostermemorychange", schema="fcm")}
    assert {
        "changed_at", "persona_id", "entity_kind", "entity_id", "operation",
        "before_text", "after_text", "cause_kind", "cause_post_id", "cause_ref",
        "reflector_run_id",
    } <= change_cols

    attr_indexes = {i["name"] for i in inspector.get_indexes("shitposterpersonaattribute", schema="fcm")}
    lore_indexes = {i["name"] for i in inspector.get_indexes("shitposterloreentry", schema="fcm")}
    assert "uq_shitposterpersonaattribute_current" in attr_indexes
    assert "ix_shitposterloreentry_current" in lore_indexes


def test_downgrade_removes_memory_tables(migration, pre_engine):
    _run(migration, "upgrade", pre_engine)
    _run(migration, "downgrade", pre_engine)
    assert not (MEMORY_TABLES & set(inspect(pre_engine).get_table_names(schema="fcm")))


def test_change_log_check_rejects_row_without_cause(engine, persona):
    with Session(engine) as s:
        s.add(
            ShitposterMemoryChange(
                changed_at=T0,
                persona_id=persona.id,
                entity_kind="attribute",
                entity_id=None,
                operation="add",
                before_text=None,
                after_text="x",
                cause_kind="operator",
                cause_post_id=None,
                cause_ref=None,
                reflector_run_id=None,
            )
        )
        with pytest.raises(IntegrityError):
            s.commit()


# --- attributes: SCD2 close/open ---------------------------------------------


def test_add_attribute_opens_current_row_and_logs_add(session, persona):
    row = dal.add_attribute(
        persona.id, "tone", "dry", cause_kind="suggestion", cause_ref="sug-1", now=T0, session=session
    )
    assert row.valid_to is None
    history = dal.change_history(persona.id, session=session)
    assert [(h.operation, h.cause_kind, h.cause_ref) for h in history] == [
        ("add", "suggestion", "sug-1")
    ]


def test_reinforce_closes_old_row_and_opens_successor(session, persona):
    first = dal.add_attribute(
        persona.id, "tone", "dry", cause_kind="post_engagement", cause_post_id=None,
        cause_ref="eng-1", now=T0, session=session,
    )
    second = dal.reinforce_attribute(
        first.id, cause_kind="post_engagement", cause_ref="eng-2", now=T1, session=session
    )
    rows = _attr_rows(session, persona.id, "tone")
    assert len(rows) == 2
    closed, current = rows
    assert closed.id == first.id
    assert closed.valid_to is not None
    assert current.id == second.id
    assert current.valid_to is None
    assert current.text == "dry"
    assert current.status == "active"


def test_only_one_current_row_per_key(session, persona):
    dal.add_attribute(persona.id, "tone", "dry", cause_kind="operator", cause_ref="op", now=T0, session=session)
    with pytest.raises(ValueError):
        dal.add_attribute(persona.id, "tone", "loud", cause_kind="operator", cause_ref="op", now=T1, session=session)


def test_retire_marks_status_and_closes_row(session, persona):
    row = dal.add_attribute(persona.id, "tone", "dry", cause_kind="suggestion", cause_ref="s", now=T0, session=session)
    retired = dal.retire_attribute(
        row.id, cause_kind="suggestion", cause_ref="s-2", now=T1, session=session
    )
    assert retired.status == "retired"
    assert retired.valid_to is None
    assert dal.active_attributes(persona.id, session=session) == []
    with pytest.raises(LookupError):
        dal.reinforce_attribute(row.id, cause_kind="suggestion", cause_ref="s-3", now=T2, session=session)


def test_merge_retires_source_as_merged_and_keeps_target(session, persona):
    src = dal.add_attribute(persona.id, "a", "fun", cause_kind="suggestion", cause_ref="s", now=T0, session=session)
    tgt = dal.add_attribute(persona.id, "b", "funny", cause_kind="suggestion", cause_ref="s", now=T0, session=session)
    merged = dal.merge_attributes(
        src.id, tgt.id, cause_kind="fold", cause_ref="fold-1", now=T1, session=session
    )
    assert merged.status == "merged"
    keys = {a.attribute_key for a in dal.active_attributes(persona.id, session=session)}
    assert keys == {"b"}
    assert dal.change_history(persona.id, session=session)[0].operation == "merge"


def test_merge_into_self_is_rejected(session, persona):
    row = dal.add_attribute(persona.id, "a", "fun", cause_kind="operator", cause_ref="op", now=T0, session=session)
    with pytest.raises(ValueError):
        dal.merge_attributes(row.id, row.id, cause_kind="fold", cause_ref="f", session=session)


# --- cause is mandatory -------------------------------------------------------


@pytest.mark.parametrize("cause_kind", ["", "unknown", None])
def test_write_without_known_cause_kind_raises(session, persona, cause_kind):
    with pytest.raises(ValueError):
        dal.add_attribute(persona.id, "tone", "dry", cause_kind=cause_kind, cause_ref="x", now=T0, session=session)
    assert _attr_rows(session, persona.id, "tone") == []
    assert dal.change_history(persona.id, session=session) == []


def test_write_without_cause_ref_or_post_raises(session, persona):
    with pytest.raises(ValueError):
        dal.add_attribute(persona.id, "tone", "dry", cause_kind="suggestion", now=T0, session=session)
    with pytest.raises(ValueError):
        dal.add_lore(persona.id, "ha", "hit", cause_kind="post_engagement", now=T0, session=session)
    assert dal.change_history(persona.id, session=session) == []


def test_reinforce_without_cause_raises_and_leaves_row_current(session, persona):
    row = dal.add_attribute(persona.id, "tone", "dry", cause_kind="operator", cause_ref="op", now=T0, session=session)
    with pytest.raises(ValueError):
        dal.reinforce_attribute(row.id, cause_kind="suggestion", now=T1, session=session)
    rows = _attr_rows(session, persona.id, "tone")
    assert len(rows) == 1 and rows[0].valid_to is None


# --- operator retire ----------------------------------------------------------


def test_operator_retire_attribute_writes_operator_cause(session, persona):
    row = dal.add_attribute(persona.id, "tone", "dry", cause_kind="suggestion", cause_ref="s", now=T0, session=session)
    change = dal.operator_retire("attribute", row.id, "U-operator", now=T1, session=session)
    assert change.cause_kind == "operator"
    assert change.cause_ref == "U-operator"
    assert change.operation == "retire"
    retired = _attr_rows(session, persona.id, "tone")[-1]
    assert retired.valid_to is None
    assert retired.status == "retired"
    assert retired.retired_by_operator is True


def test_operator_retire_lore_sets_flag_on_closed_row(session, persona):
    lore = dal.add_lore(persona.id, "running joke", "hit", cause_kind="post_engagement", cause_ref="p-1", now=T0, session=session)
    change = dal.operator_retire("lore", lore.id, "U-operator", now=T1, session=session)
    assert change.cause_kind == "operator"
    assert dal.active_lore(persona.id, session=session) == []
    closed = session.get(type(lore), lore.id)
    assert closed.valid_to is not None
    assert closed.retired_by_operator is True


def test_operator_retire_requires_subject_and_known_kind(session, persona):
    row = dal.add_attribute(persona.id, "tone", "dry", cause_kind="operator", cause_ref="op", now=T0, session=session)
    with pytest.raises(ValueError):
        dal.operator_retire("attribute", row.id, "", now=T1, session=session)
    with pytest.raises(ValueError):
        dal.operator_retire("persona", row.id, "U-op", now=T1, session=session)
    assert _attr_rows(session, persona.id, "tone")[0].valid_to is None


# --- lore ---------------------------------------------------------------------


def test_add_and_retire_lore_logs_each_op(session, persona):
    lore = dal.add_lore(persona.id, "the duck", "hit", cause_kind="post_engagement", cause_ref="p-9", now=T0, session=session)
    assert [l.id for l in dal.active_lore(persona.id, session=session)] == [lore.id]
    dal.retire_lore(lore.id, cause_kind="suggestion", cause_ref="s-7", now=T1, session=session)
    assert dal.active_lore(persona.id, session=session) == []
    ops = [h.operation for h in dal.change_history(persona.id, session=session)]
    assert ops == ["retire", "add"]


def test_fold_lore_consolidates_sources_with_summed_popularity(session, persona):
    a = dal.add_lore(persona.id, "duck one", "hit", cause_kind="post_engagement", cause_ref="p-1", now=T0, session=session)
    b = dal.add_lore(persona.id, "duck two", "hit", cause_kind="post_engagement", cause_ref="p-2", now=T0, session=session)
    a.popularity_score = 3
    b.popularity_score = 4
    session.add(a)
    session.add(b)
    session.commit()

    merged = dal.fold_lore([a.id, b.id], "the duck saga", cause_ref="fold-run-1", now=T1, session=session)
    assert merged.kind == "consolidated"
    assert merged.popularity_score == 7
    assert [l.id for l in dal.active_lore(persona.id, session=session)] == [merged.id]

    fold_rows = [h for h in dal.change_history(persona.id, session=session) if h.operation == "fold"]
    assert len(fold_rows) == 2
    assert all(h.cause_kind == "fold" and h.cause_ref == "fold-run-1" for h in fold_rows)


def test_fold_requires_sources_and_cause_ref(session, persona):
    a = dal.add_lore(persona.id, "x", "hit", cause_kind="operator", cause_ref="op", now=T0, session=session)
    with pytest.raises(ValueError):
        dal.fold_lore([], "y", cause_ref="f", session=session)
    with pytest.raises(ValueError):
        dal.fold_lore([a.id], "y", cause_ref="", session=session)
    assert dal.active_lore(persona.id, session=session)[0].id == a.id


# --- seed, history, point-in-time ----------------------------------------------


def test_seed_is_idempotent_and_operator_caused(session, persona):
    first = dal.seed_persona_attributes(persona.id, "base persona text", now=T0, session=session)
    again = dal.seed_persona_attributes(persona.id, "different text", now=T1, session=session)
    assert again.id == first.id
    assert [a.attribute_key for a in dal.active_attributes(persona.id, session=session)] == [dal.SEED_ATTRIBUTE_KEY]
    seeds = [h for h in dal.change_history(persona.id, session=session) if h.operation == "add"]
    assert len(seeds) == 1
    assert seeds[0].cause_kind == "operator"
    assert seeds[0].cause_ref == dal.SEED_CAUSE_REF


def test_change_history_is_newest_first(session, persona):
    row = dal.add_attribute(persona.id, "tone", "dry", cause_kind="operator", cause_ref="op", now=T0, session=session)
    successor = dal.reinforce_attribute(row.id, cause_kind="operator", cause_ref="op", now=T1, session=session)
    dal.retire_attribute(successor.id, cause_kind="operator", cause_ref="op", now=T2, session=session)
    history = dal.change_history(persona.id, session=session)
    assert [(h.operation, _naive(h.changed_at)) for h in history] == [
        ("retire", _naive(T2)),
        ("reinforce", _naive(T1)),
        ("add", _naive(T0)),
    ]


def test_attributes_as_of_returns_row_current_at_that_time(session, persona):
    first = dal.add_attribute(persona.id, "tone", "dry", cause_kind="operator", cause_ref="op", now=T0, session=session)
    second = dal.reinforce_attribute(first.id, cause_kind="operator", cause_ref="op", now=T2, session=session)

    before = dal.attributes_as_of(persona.id, T1, session=session)
    assert [a.id for a in before] == [first.id]
    after = dal.attributes_as_of(persona.id, T3, session=session)
    assert [a.id for a in after] == [second.id]
