"""The slack reaction migration chains after the control-plane head and round-trips.

The migration's parent is asserted; the head is derived, never pinned. The
upgrade/downgrade run through alembic's real Operations machinery against a
schema holding just `slackmessage` (the FK target).

Red-proof: dropping the table or the active-row index from upgrade() turns the
schema test red; removing a drop from downgrade() turns the round-trip red;
re-parenting the migration turns the parent test red.
"""

import importlib.util
import os
from pathlib import Path

import pytest
from alembic.operations import Operations
from alembic.runtime.migration import MigrationContext
from alembic.script import ScriptDirectory
from sqlalchemy import event, inspect
from sqlalchemy.pool import StaticPool
from sqlmodel import create_engine

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackMessage,
)

REVISION = "e5b8d3f1a6c4"
TABLES = frozenset({"slackreaction"})
_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"


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
def migration():
    matches = sorted(_versions_dir().glob(f"*{REVISION}*.py"))
    assert len(matches) == 1
    spec = importlib.util.spec_from_file_location("slack_reaction_migration", matches[0])
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture
def engine():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=[SlackMessage.__table__])
    return engine


def _run(migration, name, engine):
    with engine.begin() as conn:
        with Operations.context(MigrationContext.configure(conn)):
            getattr(migration, name)()


def test_chain_has_one_head_and_parent_is_control_plane():
    script = ScriptDirectory(str(_versions_dir().parent))
    assert len(script.get_heads()) == 1
    assert script.get_revision(REVISION).down_revision == "d4a9c1e7b3f2"


def test_upgrade_creates_table_and_active_index(migration, engine):
    _run(migration, "upgrade", engine)
    inspector = inspect(engine)
    assert TABLES <= set(inspector.get_table_names(schema="fcm"))
    cols = {c["name"] for c in inspector.get_columns("slackreaction", schema="fcm")}
    assert {
        "slack_message_id",
        "slack_channel_slack_id",
        "message_ts",
        "slack_user_slack_id",
        "emoji",
        "added_at",
        "removed_at",
        "added_by_backfill",
        "removed_by_backfill",
        "is_bot",
    } <= cols
    names = {i["name"] for i in inspector.get_indexes("slackreaction", schema="fcm")}
    assert "uq_slackreaction_active" in names


def test_downgrade_removes_the_table(migration, engine):
    _run(migration, "upgrade", engine)
    _run(migration, "downgrade", engine)
    assert not (TABLES & set(inspect(engine).get_table_names(schema="fcm")))
