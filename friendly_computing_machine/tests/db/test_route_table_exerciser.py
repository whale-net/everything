"""Liveness signal for the LB1 route tables and their type-to-channel lookup.

`slackspecialchanneltype` / `slackspecialchannel` and the two lookup helpers are
retained deliberately even though the manman V1 removal leaves them with zero
call sites, and the next milestone turns them into FCM's generic route table.
Retention stated in prose does not survive a dead-code-removal pass, so this
module keeps three independently falsifiable things true:

  1. the migration chain still creates both tables -- read from the migration
     source on disk, never from model metadata or a built schema,
  2. the schema exercised below is built only from tables the migration chain
     creates,
  3. the type-to-channel lookup resolves through those tables.

Each of the three fails on its own if its subject goes away.
"""

import ast
import os
from pathlib import Path

import pytest
from sqlalchemy import event, inspect
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine

from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_dal import (
    get_slack_special_channel_type_from_name,
    get_slack_special_channels_from_type,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackSpecialChannel,
    SlackSpecialChannelType,
)

# The two tables M2 turns into the route table.
ROUTE_TABLES = frozenset({"slackspecialchanneltype", "slackspecialchannel"})

# `slackchannel` is a foreign key target of the route table, so the exercised
# schema is only meaningful alongside it. The enum vocabulary is M2's to
# choose, so route types are written as plain strings here.
EXERCISED_MODELS = (SlackChannel, SlackSpecialChannelType, SlackSpecialChannel)

_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"

# The revision that creates the route tables, and the file it lives in. Both
# are pinned: an applied migration is never renamed, so a mismatch means the
# chain was reworked out from under a load-bearing table.
LB1_REVISION = "71e2c8de4b19"
LB1_MIGRATION_NAME = "2025_05_28_0131-71e2c8de4b19_.py"


def _versions_dir() -> Path:
    """The alembic versions/ directory holding the migration chain.

    Under a Bazel `py_test` the runfiles copy wins, so the target's `data`
    dependency is what puts the chain in front of the test. A repo-root
    `pytest` run falls back to walking up from this file, which lands on the
    same workspace-relative layout.
    """
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
    raise RuntimeError(
        f"could not locate {_VERSIONS_REL} from {__file__} or TEST_SRCDIR; the "
        "route-table exerciser must be bound to the real migration chain"
    )


def _tables_op(migration: Path, func_name: str, op_name: str) -> set[str]:
    """Table-name literals `func_name` passes to `op.<op_name>()`.

    Only that one alembic function is walked, so a table created in upgrade()
    is not cancelled out by the drop in its own downgrade().
    """
    tree = ast.parse(migration.read_text(), filename=str(migration))
    names = set()
    for func in ast.walk(tree):
        if not isinstance(func, ast.FunctionDef) or func.name != func_name:
            continue
        for call in ast.walk(func):
            if not isinstance(call, ast.Call):
                continue
            if not (isinstance(call.func, ast.Attribute) and call.func.attr == op_name):
                continue
            if call.args and isinstance(call.args[0], ast.Constant):
                if isinstance(call.args[0].value, str):
                    names.add(call.args[0].value)
    return names


def _revision_id(migration: Path) -> str | None:
    """The `revision` id a migration module declares at module level."""
    for node in ast.parse(migration.read_text(), filename=str(migration)).body:
        if not isinstance(node, ast.AnnAssign):
            continue
        if getattr(node.target, "id", None) != "revision":
            continue
        if isinstance(node.value, ast.Constant):
            return node.value.value
    return None


@pytest.fixture(scope="module")
def migrated_tables() -> set[str]:
    """Table names the whole versioned migration chain leaves in the schema.

    Walked in filename order, so a table dropped by a later revision leaves the
    set. A table a revision both creates and drops is the same table either
    side of the revision, not a lifecycle, so its own drop does not count.
    """
    present: set[str] = set()
    for migration in sorted(_versions_dir().glob("*.py")):
        created = _tables_op(migration, "upgrade", "create_table")
        present = (present | created) - (
            _tables_op(migration, "downgrade", "drop_table") - created
        )
    return present


def _engine():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    return engine


@pytest.fixture
def session():
    engine = _engine()
    Base.metadata.create_all(engine, tables=[m.__table__ for m in EXERCISED_MODELS])
    with Session(engine) as s:
        yield s


# --- check 1: the LB1 migration is still here and still declares the tables -


@pytest.fixture(scope="module")
def lb1_migration() -> Path:
    """The one revision that creates the route tables, pinned by revision id.

    Pinning the id and the exact filename is the point: a migration that is
    deleted, renamed, or emptied stops matching here even if some other
    revision still happens to create a table of the same name.
    """
    versions = _versions_dir()
    matches = sorted(versions.glob(f"*{LB1_REVISION}*.py"))
    expected = versions / LB1_MIGRATION_NAME
    assert matches == [expected], (
        f"expected exactly one migration for revision {LB1_REVISION} at "
        f"{expected}, found {[m.name for m in matches]}; the route tables are "
        "retained for the next milestone's routing work, and the revision that "
        "creates them must not be deleted, renamed, or emptied"
    )
    return matches[0]


def test_lb1_migration_still_creates_both_route_tables(lb1_migration):
    assert _revision_id(lb1_migration) == LB1_REVISION, (
        f"{lb1_migration.name} no longer declares revision {LB1_REVISION}"
    )
    created = _tables_op(lb1_migration, "upgrade", "create_table")
    missing = sorted(ROUTE_TABLES - created)
    assert not missing, (
        f"{lb1_migration.name} no longer creates {missing}; it still creates "
        f"{sorted(created)}. The route tables are retained for the next "
        "milestone's routing work"
    )


# --- check 2: the exercised schema is made of migrated tables ---------------


def test_exercised_schema_exists_in_the_migration_chain(migrated_tables):
    engine = _engine()
    Base.metadata.create_all(engine, tables=[m.__table__ for m in EXERCISED_MODELS])
    exercised = set(inspect(engine).get_table_names(schema="fcm"))

    assert exercised == {m.__table__.name for m in EXERCISED_MODELS}, (
        "the schema this exerciser drives is not the expected set of tables"
    )
    assert ROUTE_TABLES <= exercised, (
        f"the route tables are missing from the exercised schema: "
        f"{sorted(ROUTE_TABLES - exercised)}"
    )
    unmigrated = exercised - migrated_tables
    assert not unmigrated, (
        f"exercised tables {sorted(unmigrated)} are not created by any migration; "
        "a schema the deployed database never gets is not a real exerciser"
    )


# --- check 3: the lookup resolves through those tables ----------------------


def _seed(session, type_name: str, friendly: str, channels: list[str]):
    session.add(
        SlackSpecialChannelType(type_name=type_name, friendly_type_name=friendly)
    )
    session.commit()
    channel_type = get_slack_special_channel_type_from_name(type_name, session=session)
    for slack_id in channels:
        channel = SlackChannel(
            slack_id=slack_id, name=slack_id, channel_type="public_channel"
        )
        session.add(channel)
        session.commit()
        session.refresh(channel)
        session.add(
            SlackSpecialChannel(
                slack_channel_id=channel.id,
                slack_special_channel_type_id=channel_type.id,
                reason=f"routed via {type_name}",
            )
        )
    session.commit()
    return channel_type


def test_lookup_resolves_a_type_to_its_channels(session):
    seeded = _seed(session, "manman_dev", "ManMan Dev", ["C_DEV_A", "C_DEV_B"])

    found = get_slack_special_channel_type_from_name("manman_dev", session=session)
    assert found is not None
    assert found.id == seeded.id
    assert found.type_name == "manman_dev"

    routes = get_slack_special_channels_from_type(found, session=session)
    assert routes is not None
    assert sorted(route.slack_channel.slack_id for route, _, _ in routes) == [
        "C_DEV_A",
        "C_DEV_B",
    ]
    assert all(
        route.slack_special_channel_type.type_name == "manman_dev"
        for route, _, _ in routes
    )
    assert all(route.enabled for route, _, _ in routes)


def test_lookup_returns_nothing_for_an_unconfigured_type(session):
    """An empty route table is a healthy state, not a bug -- say so in a test."""
    _seed(session, "manman_dev", "ManMan Dev", ["C_DEV_A"])

    assert (
        get_slack_special_channel_type_from_name("no_such_type", session=session)
        is None
    )
    absent = SlackSpecialChannelType(
        id=4242, type_name="no_such_type", friendly_type_name="No Such Type"
    )
    assert get_slack_special_channels_from_type(absent, session=session) == []


def test_lookup_returns_only_the_channels_of_its_own_type(session):
    """The second lookup filters by type rather than returning every row."""
    _seed(session, "manman_dev", "ManMan Dev", ["C_DEV_A"])
    other = _seed(session, "other_type", "Other", ["C_OTHER_A", "C_OTHER_B"])

    routes = get_slack_special_channels_from_type(other, session=session)
    assert [route.slack_channel.slack_id for route, _, _ in routes] == [
        "C_OTHER_A",
        "C_OTHER_B",
    ]

    dev = get_slack_special_channel_type_from_name("manman_dev", session=session)
    dev_routes = get_slack_special_channels_from_type(dev, session=session)
    assert [route.slack_channel.slack_id for route, _, _ in dev_routes] == ["C_DEV_A"]
