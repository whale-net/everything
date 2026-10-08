"""Shitposter persona/post migration and DAL.

Red-proof: dropping the partial unique index, the (channel, ts) unique
constraint, the seed insert, the valid_to close in add_persona_revision,
or the `thread_owner IS NULL` guard turns the matching test red.
"""

import datetime
import importlib.util
import os
from pathlib import Path
from types import ModuleType

import pytest
from alembic.operations import Operations
from alembic.runtime.migration import MigrationContext
from sqlalchemy import event, inspect
from sqlalchemy.exc import IntegrityError
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models import (  # noqa: F401
    base,
    genai,
    music_poll,
    poll,
    scheduled_poll,
    shitposter,
    slack,
    task,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
)

MIGRATION_REVISION = "d4a9b2c7e1f3"
SHITPOSTER_TABLES = frozenset(
    {"shitposterpersona", "shitposterpersonarevision", "shitposterpost"}
)
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


def _load(path: Path) -> ModuleType:
    spec = importlib.util.spec_from_file_location(f"mig_{path.stem}", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.fixture(scope="module")
def migration() -> ModuleType:
    matches = sorted(_versions_dir().glob(f"*{MIGRATION_REVISION}*.py"))
    assert len(matches) == 1
    module = _load(matches[0])
    assert module.revision == MIGRATION_REVISION
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
        # Postgres NOW() default used by the revision table
        dbapi_conn.create_function(
            "NOW", 0, lambda: datetime.datetime.now(datetime.UTC).isoformat(" ")
        )

    Base.metadata.create_all(
        engine,
        tables=[t for t in Base.metadata.sorted_tables if t.name not in SHITPOSTER_TABLES],
    )
    return engine


@pytest.fixture
def engine(pre_engine, migration):
    _run(migration, "upgrade", pre_engine)
    # the ORM maps the post's snapshot column; this revision predates it
    with pre_engine.begin() as conn:
        conn.exec_driver_sql(
            "ALTER TABLE fcm.shitposterpost ADD COLUMN context_snapshot_id INTEGER"
        )
    return pre_engine


@pytest.fixture
def session(engine):
    with Session(engine) as s:
        yield s


@pytest.fixture
def channel(session) -> SlackChannel:
    ch = SlackChannel(slack_id="C1", name="general", channel_type="public")
    session.add(ch)
    session.commit()
    session.refresh(ch)
    return ch


def _post(session, channel, persona, revision, ts="1.1", **kw):
    args = dict(
        slack_channel_id=channel.id,
        slack_message_ts=ts,
        persona_id=persona.id,
        persona_revision_id=revision.id,
        trigger="summon",
        principal_iss="https://iss.example",
        principal_sub="user-1",
        principal_kind="human",
        whagent_session_id="sess-1",
        session=session,
    )
    args.update(kw)
    return dal.record_post(**args)


# --- migration ---


def test_migration_chain_links_to_an_existing_revision(migration):
    revisions = {
        _load(p).revision for p in _versions_dir().glob("*.py") if p.stem != "__init__"
    }
    assert migration.down_revision in revisions


def test_migration_creates_tables_and_downgrade_removes_them(pre_engine, migration):
    _run(migration, "upgrade", pre_engine)
    assert SHITPOSTER_TABLES <= set(inspect(pre_engine).get_table_names(schema="fcm"))
    _run(migration, "downgrade", pre_engine)
    assert not (SHITPOSTER_TABLES & set(inspect(pre_engine).get_table_names(schema="fcm")))


def test_model_metadata_declares_the_migrated_tables():
    names = {t.name for t in Base.metadata.sorted_tables}
    assert SHITPOSTER_TABLES <= names


# --- persona seed and SCD2 ---


def test_seeded_persona_has_exactly_one_current_revision(session):
    persona = dal.get_default_persona(session=session)
    assert persona is not None
    current = dal.get_current_persona_revision(persona.id, session=session)
    assert current is not None and current.persona_text
    assert current.valid_to is None
    assert len(dal.list_persona_revisions(persona.id, session=session)) == 1


def test_add_revision_closes_prior_and_keeps_it_queryable(session):
    persona = dal.get_default_persona(session=session)
    first = dal.get_current_persona_revision(persona.id, session=session)
    second = dal.add_persona_revision(
        persona.id, "new text", cause_kind="feedback", cause_ref="r1", session=session
    )
    revisions = dal.list_persona_revisions(persona.id, session=session)
    assert [r.id for r in revisions] == [first.id, second.id]
    assert revisions[0].valid_to is not None
    assert revisions[1].valid_to is None
    assert dal.get_current_persona_revision(persona.id, session=session).id == second.id
    assert revisions[1].cause_kind == "feedback"


def test_second_open_revision_is_rejected(session):
    persona = dal.get_default_persona(session=session)
    session.add(
        shitposter.ShitposterPersonaRevision(persona_id=persona.id, persona_text="x")
    )
    with pytest.raises(IntegrityError):
        session.commit()


# --- posts ---


def test_record_post_round_trips(session, channel):
    persona = dal.get_default_persona(session=session)
    rev = dal.get_current_persona_revision(persona.id, session=session)
    _post(session, channel, persona, rev, trigger="scheduled", principal_kind="service")
    got = dal.get_post_by_channel_ts(channel.id, "1.1", session=session)
    assert got.trigger == "scheduled"
    assert got.principal_kind == "service"
    assert got.principal_iss == "https://iss.example"
    assert got.principal_sub == "user-1"
    assert got.persona_revision_id == rev.id
    assert got.whagent_session_id == "sess-1"
    assert dal.get_post_by_channel_ts(channel.id, "9.9", session=session) is None


def test_duplicate_channel_ts_is_rejected(session, channel):
    persona = dal.get_default_persona(session=session)
    rev = dal.get_current_persona_revision(persona.id, session=session)
    _post(session, channel, persona, rev)
    with pytest.raises(IntegrityError):
        _post(session, channel, persona, rev)


def test_invalid_trigger_is_rejected(session, channel):
    persona = dal.get_default_persona(session=session)
    rev = dal.get_current_persona_revision(persona.id, session=session)
    with pytest.raises(IntegrityError):
        _post(session, channel, persona, rev, trigger="bogus")


def test_set_thread_owner_first_caller_wins(session, channel):
    persona = dal.get_default_persona(session=session)
    rev = dal.get_current_persona_revision(persona.id, session=session)
    post = _post(session, channel, persona, rev, trigger="scheduled")
    assert dal.set_thread_owner_if_unset(post.id, "U1", session=session) is True
    assert dal.set_thread_owner_if_unset(post.id, "U2", session=session) is False
    session.expire_all()
    got = dal.get_post_by_channel_ts(channel.id, "1.1", session=session)
    assert got.thread_owner_slack_user_id == "U1"


def test_set_thread_owner_does_not_override_summoner(session, channel):
    persona = dal.get_default_persona(session=session)
    rev = dal.get_current_persona_revision(persona.id, session=session)
    post = _post(session, channel, persona, rev, thread_owner_slack_user_id="U0")
    assert dal.set_thread_owner_if_unset(post.id, "U1", session=session) is False
