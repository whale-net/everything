"""The scheduled-poll history migration round-trips (krill M5).

The head revision adds `poll.automated` and the scheduledpollrun /
scheduledpollrunoption tables. The versioned chain as a whole is
Postgres-only (`create schema fcm`, a public-schema version table),
so this exerciser drives the head revision's own upgrade() and
downgrade() through alembic's real Operations machinery against the
schema as it stands at the revision's down_revision -- built from
the model metadata minus what the head revision adds.

Red-proof: dropping the unique constraint, an index, or the
`automated` column from the migration turns the schema assertions
red; removing the downgrade's drop_column/drop_table calls turns
the restore assertion red.
"""

import importlib.util
import os
from pathlib import Path
from types import ModuleType

import pytest
import sqlalchemy as sa
from alembic.operations import Operations
from alembic.runtime.migration import MigrationContext
from sqlalchemy import event, inspect
from sqlalchemy.pool import StaticPool
from sqlmodel import create_engine

from friendly_computing_machine.src.friendly_computing_machine.models import (  # noqa: F401
    base,
    genai,
    music_poll,
    poll,
    scheduled_poll,
    slack,
    task,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
)
from friendly_computing_machine.src.friendly_computing_machine.models.scheduled_poll import (
    ScheduledPollRun,
    ScheduledPollRunOption,
)

# The head revision, pinned by id and filename: an applied
# migration is never renamed, so a mismatch means the chain was
# reworked out from under this schema.
HEAD_REVISION = "c7d2e4f6a8b1"
HEAD_MIGRATION_NAME = "2026_10_04_0000-c7d2e4f6a8b1_scheduled_poll.py"
DOWN_REVISION = "a3e7d51c9b42"

HEAD_TABLES = frozenset({"scheduledpollrun", "scheduledpollrunoption"})

_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"


def _versions_dir() -> Path:
    """The alembic versions/ directory holding the migration chain.

    Under a Bazel `py_test` the runfiles copy wins, so the target's
    `data` dependency is what puts the chain in front of the test. A
    repo-root `pytest` run falls back to walking up from this file,
    which lands on the same workspace-relative layout.
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
        f"could not locate {_VERSIONS_REL} from {__file__} or TEST_SRCDIR; "
        "the migration exerciser must be bound to the real migration chain"
    )


@pytest.fixture(scope="module")
def head_migration() -> ModuleType:
    versions = _versions_dir()
    matches = sorted(versions.glob(f"*{HEAD_REVISION}*.py"))
    expected = versions / HEAD_MIGRATION_NAME
    assert matches == [expected], (
        f"expected exactly one migration for revision {HEAD_REVISION} at "
        f"{expected}, found {[m.name for m in matches]}"
    )
    migration = matches[0]

    spec = importlib.util.spec_from_file_location("head_migration", migration)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)

    assert module.revision == HEAD_REVISION
    assert module.down_revision == DOWN_REVISION, (
        f"{migration.name} no longer revises {DOWN_REVISION}; the "
        "scheduled-poll history must stay at the head of the chain"
    )
    return module


def _copy_column(column: sa.Column) -> sa.Column:
    return sa.Column(
        column.name,
        column.type,
        primary_key=column.primary_key,
        nullable=column.nullable,
        unique=column.unique,
        index=column.index,
        server_default=column.server_default,
    )


def _without_column(table: sa.Table, column_name: str) -> sa.Table:
    return sa.Table(
        table.name,
        sa.MetaData(),
        *[_copy_column(c) for c in table.columns if c.name != column_name],
        schema=table.schema,
    )


@pytest.fixture
def engine():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    # the schema at the head revision's down_revision: every
    # model table except the two the head revision creates, with
    # `poll` still lacking its new `automated` column
    poll = Poll.__table__
    pre_head_tables = [
        table
        for table in Base.metadata.sorted_tables
        if table.name not in HEAD_TABLES and table is not poll
    ] + [_without_column(poll, "automated")]
    Base.metadata.create_all(engine, tables=pre_head_tables)
    return engine


def _run(head_migration, func_name: str, engine) -> None:
    """Run one of the migration's functions through alembic's
    real operation machinery -- the same `op` proxy the alembic
    runtime installs when it executes a revision."""
    with engine.begin() as conn:
        context = MigrationContext.configure(conn)
        with Operations.context(context):
            getattr(head_migration, func_name)()


def _columns(engine, table: str) -> list[str]:
    return [c["name"] for c in inspect(engine).get_columns(table, schema="fcm")]


def test_head_migration_upgrade_applies_cleanly(head_migration, engine):
    _run(head_migration, "upgrade", engine)

    inspector = inspect(engine)
    assert HEAD_TABLES <= set(inspector.get_table_names(schema="fcm"))

    # the poll table gains the automated flag the rendering task consumes
    assert "automated" in _columns(engine, "poll")

    run_columns = _columns(engine, "scheduledpollrun")
    for column in (
        "run_identity",
        "slack_channel_slack_id",
        "run_at",
        "scheduled_fire_time",
        "workflow_run_id",
        "poll_id",
        "slack_message_ts",
        "created_at",
    ):
        assert column in run_columns

    option_columns = _columns(engine, "scheduledpollrunoption")
    for column in (
        "scheduled_poll_run_id",
        "poll_option_id",
        "position",
        "song_identity",
        "song_link",
        "submitter_slack_user_slack_id",
        "submission_date",
    ):
        assert column in option_columns

    # the indexes the model declares, for model/migration parity
    run_indexes = {i["name"] for i in inspector.get_indexes("scheduledpollrun", schema="fcm")}
    assert {
        "ix_fcm_scheduledpollrun_run_identity",
        "ix_fcm_scheduledpollrun_slack_channel_slack_id",
        "ix_fcm_scheduledpollrun_poll_id",
    } <= run_indexes

    option_indexes = {
        i["name"]
        for i in inspector.get_indexes("scheduledpollrunoption", schema="fcm")
    }
    assert {
        "ix_fcm_scheduledpollrunoption_scheduled_poll_run_id",
        "ix_fcm_scheduledpollrunoption_poll_option_id",
        "ix_fcm_scheduledpollrunoption_submitter_slack_user_slack_id",
    } <= option_indexes

    # the unique keys that make a retried or replayed run record
    # at most one row per channel, and one row per option position
    run_uniques = {
        tuple(sorted(u["column_names"]))
        for u in inspector.get_unique_constraints("scheduledpollrun", schema="fcm")
    }
    assert ("run_identity", "slack_channel_slack_id") in run_uniques
    option_uniques = {
        tuple(sorted(u["column_names"]))
        for u in inspector.get_unique_constraints(
            "scheduledpollrunoption", schema="fcm"
        )
    }
    assert ("position", "scheduled_poll_run_id") in option_uniques


def test_head_migration_downgrade_restores_the_prior_schema(
    head_migration, engine
):
    _run(head_migration, "upgrade", engine)
    _run(head_migration, "downgrade", engine)

    inspector = inspect(engine)
    fcm_tables = set(inspector.get_table_names(schema="fcm"))
    assert not (HEAD_TABLES & fcm_tables)
    assert "automated" not in _columns(engine, "poll")


def test_model_metadata_and_migration_declare_the_same_tables():
    """The migration creates exactly the tables the models declare,
    so `alembic check` against a migrated database stays clean."""
    model_tables = {
        ScheduledPollRun.__table__.name,
        ScheduledPollRunOption.__table__.name,
    }
    assert model_tables == HEAD_TABLES
