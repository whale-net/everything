"""Stable lore_key: migration backfill across SCD2 revision chains and DAL carry-forward.

Runs against SQLite with the memory migration and the lore_key migration applied
through alembic's real Operations machinery. Pre-migration lore is seeded with
raw SQL, since the model already carries lore_key.

Red-proof: widening the backfill's reinforce/promote operation filter in its
chain SQL, or dropping lore_key=current.lore_key in _supersede_lore, turns the
matching test red.
"""

import datetime
import importlib.util
import os
import uuid
from pathlib import Path
from types import ModuleType

import pytest
from alembic.operations import Operations
from alembic.runtime.migration import MigrationContext
from sqlalchemy import event, inspect, text
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models import (  # noqa: F401
    base,
    shitposter,
    shitposter_context,
    shitposter_memory,
    slack,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterPersona,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
)

MEMORY_REVISION = "e5b0d3a8c2f5"
LORE_KEY_REVISION = "b7039c37db2c"
MEMORY_TABLES = frozenset(
    {"shitposterpersonaattribute", "shitposterloreentry", "shitpostermemorychange"}
)
_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"

T0 = datetime.datetime(2026, 10, 1, 12, 0, tzinfo=datetime.UTC)
T1 = T0 + datetime.timedelta(hours=1)
T2 = T0 + datetime.timedelta(hours=2)


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


def _load(revision: str) -> ModuleType:
    matches = sorted(_versions_dir().glob(f"*{revision}*.py"))
    assert len(matches) == 1
    spec = importlib.util.spec_from_file_location(f"mig_{revision}", matches[0])
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    assert module.revision == revision
    return module


@pytest.fixture(scope="module")
def memory_migration() -> ModuleType:
    return _load(MEMORY_REVISION)


@pytest.fixture(scope="module")
def lore_key_migration() -> ModuleType:
    return _load(LORE_KEY_REVISION)


def _run(migration, func_name: str, engine) -> None:
    with engine.begin() as conn:
        with Operations.context(MigrationContext.configure(conn)):
            getattr(migration, func_name)()


@pytest.fixture
def pre_engine(memory_migration):
    """Memory tables at their pre-lore_key shape."""
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
    _run(memory_migration, "upgrade", engine)
    return engine


# --- migration backfill --------------------------------------------------------

_TS = {0: "2026-10-01 12:00:00", 1: "2026-10-01 13:00:00", 2: "2026-10-01 14:00:00"}


def _lore(conn, lore_id, persona_id, kind, valid_from, valid_to=None):
    conn.execute(
        text(
            "INSERT INTO fcm.shitposterloreentry (id, persona_id, text, kind, "
            "popularity_score, retired_by_operator, valid_from, valid_to) "
            "VALUES (:id, :p, :t, :k, 0, 0, :vf, :vt)"
        ),
        {
            "id": lore_id,
            "p": persona_id,
            "t": f"lore {lore_id}",
            "k": kind,
            "vf": _TS[valid_from],
            "vt": None if valid_to is None else _TS[valid_to],
        },
    )


def _change(conn, persona_id, entity_id, operation, at):
    cause = "fold" if operation == "fold" else "post_engagement"
    conn.execute(
        text(
            "INSERT INTO fcm.shitpostermemorychange (changed_at, persona_id, "
            "entity_kind, entity_id, operation, cause_kind, cause_ref) "
            "VALUES (:at, :p, 'lore', :e, :op, :c, 'seed')"
        ),
        {"at": _TS[at], "p": persona_id, "e": entity_id, "op": operation, "c": cause},
    )


def _snapshot_item(conn, item_id, lore_id):
    conn.execute(
        text(
            "INSERT INTO fcm.shitpostersnapshotitem (id, snapshot_id, item_kind, "
            "item_id, rank, is_random_pick) VALUES (:id, 1, 'lore', :l, 1, 0)"
        ),
        {"id": item_id, "l": lore_id},
    )


@pytest.fixture
def seeded(pre_engine):
    """Persona 1: chain 1->2->3 (reinforce, promote), unlinked 4 opening at the
    same instant 1 closed, fold source 5 -> consolidated 6, and a lone row 7.
    Persona 2: row 8 opening when 1 closed (different persona, never linked).
    Persona 3: 9 retired at the instant 10 opened (not a revision link)."""
    with pre_engine.begin() as conn:
        _lore(conn, 1, 1, "hit", 0, 1)
        _change(conn, 1, 1, "reinforce", 1)
        _lore(conn, 2, 1, "hit", 1, 2)
        _change(conn, 1, 2, "promote", 2)
        _lore(conn, 3, 1, "hit", 2)
        _lore(conn, 4, 1, "hit", 1)
        _lore(conn, 5, 1, "hit", 0, 1)
        _change(conn, 1, 5, "fold", 1)
        _lore(conn, 6, 1, "consolidated", 1)
        _lore(conn, 7, 1, "hit", 0)
        _lore(conn, 8, 2, "hit", 1)
        _lore(conn, 9, 3, "hit", 0, 1)
        _change(conn, 3, 9, "retire", 1)
        _lore(conn, 10, 3, "hit", 1)
        _snapshot_item(conn, 1, 1)
        _snapshot_item(conn, 2, 3)
        _snapshot_item(conn, 3, 7)
    return pre_engine


def _keys(engine) -> dict[int, uuid.UUID]:
    with engine.connect() as conn:
        rows = conn.execute(
            text("SELECT id, lore_key FROM fcm.shitposterloreentry")
        ).all()
    return {row_id: uuid.UUID(str(key)) for row_id, key in rows}


def test_upgrade_shares_key_across_revision_chain(seeded, lore_key_migration):
    _run(lore_key_migration, "upgrade", seeded)
    keys = _keys(seeded)
    assert keys[1] == keys[2] == keys[3]
    assert keys[1] == lore_key_migration.lore_key_for_root(1)


def test_upgrade_gives_unlinked_rows_distinct_keys(seeded, lore_key_migration):
    _run(lore_key_migration, "upgrade", seeded)
    keys = _keys(seeded)
    assert None not in keys.values()
    # 4 opens with chain row 2, 6 came from a fold, 8 is another persona, 9 was retired
    distinct = [keys[i] for i in (1, 4, 5, 6, 7, 8, 9, 10)]
    assert len(set(distinct)) == len(distinct)
    for row_id in (4, 5, 6, 7, 8, 9, 10):
        assert keys[row_id] == lore_key_migration.lore_key_for_root(row_id)


def test_upgrade_adds_not_null_column_and_index(seeded, lore_key_migration):
    _run(lore_key_migration, "upgrade", seeded)
    inspector = inspect(seeded)
    cols = {c["name"]: c for c in inspector.get_columns("shitposterloreentry", schema="fcm")}
    assert cols["lore_key"]["nullable"] is False
    indexes = {i["name"]: i for i in inspector.get_indexes("shitposterloreentry", schema="fcm")}
    assert indexes[lore_key_migration.INDEX]["column_names"] == ["persona_id", "lore_key"]


def test_backfilled_keys_join_snapshot_items_across_revisions(seeded, lore_key_migration):
    _run(lore_key_migration, "upgrade", seeded)
    keys = _keys(seeded)
    with Session(seeded) as s:
        items = dal.snapshot_items_for_key(keys[1], session=s)
        assert [i.item_id for i in items] == [1, 3]
        assert [i.item_id for i in dal.snapshot_items_for_key(keys[7], session=s)] == [7]
        assert dal.snapshot_items_for_key(keys[4], session=s) == []
        revisions = dal.revisions_for_key(1, keys[1], session=s)
        assert [r.id for r in revisions] == [1, 2, 3]
        assert dal.revisions_for_key(2, keys[1], session=s) == []


def test_downgrade_drops_lore_key(seeded, lore_key_migration):
    _run(lore_key_migration, "upgrade", seeded)
    _run(lore_key_migration, "downgrade", seeded)
    inspector = inspect(seeded)
    cols = {c["name"] for c in inspector.get_columns("shitposterloreentry", schema="fcm")}
    assert "lore_key" not in cols
    indexes = {i["name"] for i in inspector.get_indexes("shitposterloreentry", schema="fcm")}
    assert lore_key_migration.INDEX not in indexes


# --- DAL -------------------------------------------------------------------------


@pytest.fixture
def session(pre_engine, lore_key_migration):
    _run(lore_key_migration, "upgrade", pre_engine)
    with Session(pre_engine) as s:
        yield s


@pytest.fixture
def persona(session) -> ShitposterPersona:
    row = ShitposterPersona(name="shitposter")
    session.add(row)
    session.commit()
    session.refresh(row)
    return row


def _add(session, persona, label, now=T0) -> ShitposterLoreEntry:
    return dal.add_lore(
        persona.id, label, "hit", "operator", cause_ref="test", now=now, session=session
    )


def test_new_revision_keeps_lore_key(session, persona):
    original = _add(session, persona, "a")
    successor = dal._supersede_lore(session, original, now=T1, popularity_score=7)
    session.commit()
    assert successor.id != original.id
    assert successor.lore_key == original.lore_key
    assert successor.popularity_score == 7
    assert successor.text == "a"
    third = dal._supersede_lore(session, successor, now=T2, text="a2")
    session.commit()
    assert third.lore_key == original.lore_key
    revisions = dal.revisions_for_key(persona.id, original.lore_key, session=session)
    assert [r.id for r in revisions] == [original.id, successor.id, third.id]
    assert [r.valid_to is None for r in revisions] == [False, False, True]


def test_supersede_rejects_closed_row(session, persona):
    original = _add(session, persona, "a")
    dal._supersede_lore(session, original, now=T1)
    session.commit()
    with pytest.raises(ValueError):
        dal._supersede_lore(session, original, now=T2)


def test_new_entries_get_fresh_keys(session, persona):
    a = _add(session, persona, "a")
    b = _add(session, persona, "b")
    assert a.lore_key != b.lore_key
    folded = dal.fold_lore([a.id, b.id], "ab", cause_ref="fold:1", now=T1, session=session)
    assert folded.lore_key not in {a.lore_key, b.lore_key}
    assert [r.id for r in dal.revisions_for_key(persona.id, a.lore_key, session=session)] == [a.id]
