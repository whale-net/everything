"""The shitposter control-plane migration is the single chain head and round-trips.

The head is derived from the alembic script directory, never pinned. The
upgrade/downgrade run through alembic's real Operations machinery against a
schema holding just `slackchannel` (the FK target).

Red-proof: dropping a table or the current-row index from upgrade() turns the
schema test red; removing a drop from downgrade() turns the round-trip red;
giving the migration a second parent turns the single-head test red.
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
    SlackChannel,
)

REVISION = "d4a9c1e7b3f2"
TABLES = frozenset({"shitposterchanneloptin", "shitposterkillswitch"})
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
    spec = importlib.util.spec_from_file_location("shitposter_migration", matches[0])
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

    Base.metadata.create_all(engine, tables=[SlackChannel.__table__])
    return engine


def _run(migration, name, engine):
    with engine.begin() as conn:
        with Operations.context(MigrationContext.configure(conn)):
            getattr(migration, name)()


def test_chain_has_one_head_and_it_is_reachable():
    script = ScriptDirectory(str(_versions_dir().parent))
    heads = script.get_heads()
    assert len(heads) == 1
    revisions = {r.revision for r in script.walk_revisions()}
    assert REVISION in revisions
    assert script.get_revision(REVISION).down_revision is not None


def test_upgrade_creates_scd2_tables(migration, engine):
    _run(migration, "upgrade", engine)
    inspector = inspect(engine)
    assert TABLES <= set(inspector.get_table_names(schema="fcm"))

    optin = {c["name"] for c in inspector.get_columns("shitposterchanneloptin", schema="fcm")}
    assert {
        "slack_channel_id",
        "opted_in",
        "set_by_slack_user_id",
        "set_by_slack_team_id",
        "valid_from",
        "valid_to",
    } <= optin
    kill = {c["name"] for c in inspector.get_columns("shitposterkillswitch", schema="fcm")}
    assert {"scope", "enabled", "set_by", "reason", "valid_from", "valid_to"} <= kill

    for table, index in (
        ("shitposterchanneloptin", "uq_shitposterchanneloptin_current"),
        ("shitposterkillswitch", "uq_shitposterkillswitch_current"),
    ):
        names = {i["name"] for i in inspector.get_indexes(table, schema="fcm")}
        assert index in names


def test_downgrade_removes_the_tables(migration, engine):
    _run(migration, "upgrade", engine)
    _run(migration, "downgrade", engine)
    assert not (TABLES & set(inspect(engine).get_table_names(schema="fcm")))
