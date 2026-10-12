"""Member identification for generated-memory guardrails.

Covers the identifier set (display name, real name, distinctive name tokens,
operator aliases, former members, bots excluded), generated_text_refusal over
it, the Slack user upsert keeping both profile names current, and the
slackuser_profile_names migration round-trip (head derived, never pinned).

Red-proof: dropping the token split from name_identifiers turns the first-name
cases red; dropping load_member_aliases from member_identifiers turns the
nickname cases red; removing the COMMON_WORDS filter turns the common-word
case red.
"""

import importlib.util
import os
from pathlib import Path
from types import ModuleType

import pytest
from alembic.operations import Operations
from alembic.runtime.migration import MigrationContext
from alembic.script import ScriptDirectory
from sqlalchemy import Column, Integer, MetaData, String, Table, event, inspect
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    slack_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackUser,
    SlackUserCreate,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    parse_member_aliases,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    guardrails,
)

REVISION = "9b2d5f7e1c48"
_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"


def _sqlite():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    return engine


@pytest.fixture
def engine(monkeypatch):
    monkeypatch.delenv("FCM_SHITPOSTER_MEMBER_ALIASES", raising=False)
    engine = _sqlite()
    Base.metadata.create_all(engine, tables=[SlackUser.__table__])
    with Session(engine) as s:
        s.add_all(
            [
                # display name stored as name, real name differs
                SlackUser(
                    slack_id="U1",
                    name="bobbyt",
                    display_name="bobbyt",
                    real_name="Robert Tables",
                ),
                # former member: no recent activity, row kept
                SlackUser(slack_id="U2", name="Zelda Quinlan", real_name="Zelda Quinlan"),
                # common-word first name: only the full name identifies her
                SlackUser(slack_id="U3", name="Grace Okonkwo", real_name="Grace Okonkwo"),
                SlackUser(slack_id="B1", name="Lorebot Prime", is_bot=True),
            ]
        )
        s.commit()
    return engine


def _identifiers(engine) -> list[str]:
    with Session(engine) as s:
        return guardrails.member_identifiers(s)


def test_name_identifiers_split_distinctive_tokens():
    assert guardrails.name_identifiers("Robert Tables") == ["Robert Tables", "Robert", "Tables"]
    assert guardrails.name_identifiers("Grace Okonkwo") == ["Grace Okonkwo", "Okonkwo"]
    assert guardrails.name_identifiers("Al Bo") == ["Al Bo"]
    assert guardrails.name_identifiers(None) == []
    assert guardrails.name_identifiers("   ") == []


def test_member_identifiers_cover_names_tokens_aliases_and_former_members(
    engine, monkeypatch
):
    monkeypatch.setenv("FCM_SHITPOSTER_MEMBER_ALIASES", " Little Bobby , ,Z-dog")
    ids = _identifiers(engine)
    lowered = {i.lower() for i in ids}
    assert {
        "bobbyt", "robert tables", "robert", "tables", "zelda quinlan", "zelda",
        "quinlan", "grace okonkwo", "okonkwo", "little bobby", "z-dog",
    } <= lowered
    assert "grace" not in lowered
    assert not any("lorebot" in i for i in lowered)
    assert len(lowered) == len(ids)
    assert [len(i) for i in ids] == sorted((len(i) for i in ids), reverse=True)


@pytest.mark.parametrize(
    "text",
    [
        "Keeps roasting Robert Tables about spreadsheets",  # real name, display stored
        "Keeps roasting Robert about spreadsheets",  # first name only
        "Thinks bobbyt is a menace",  # stored display name
        "Thinks Zelda never logs off",  # former member
        "Thinks Okonkwo is always late",  # surname of a common-word first name
        "Treats little bobby as a legend",  # configured nickname
        "Blames z-dog for the outage",  # configured nickname with punctuation
        "Still mad at <@U9> about soup",  # raw mention
    ],
)
def test_generated_text_naming_a_member_is_rejected(engine, monkeypatch, text):
    monkeypatch.setenv("FCM_SHITPOSTER_MEMBER_ALIASES", "Little Bobby,Z-dog")
    refusal = guardrails.generated_text_refusal(text, _identifiers(engine), [])
    assert refusal == guardrails.REASON_TARGETS_MEMBER


@pytest.mark.parametrize(
    "text",
    [
        "Says grace before every meal",  # common-word first name alone
        "Robertson is a fine screwdriver",  # token is not a whole word here
        "Treats Mondays as a personal enemy",
    ],
)
def test_unrelated_text_is_not_rejected(engine, text):
    assert guardrails.generated_text_refusal(text, _identifiers(engine), []) is None


def test_parse_member_aliases_strips_and_drops_blanks():
    assert parse_member_aliases(" a ,, Big Al ,") == ["a", "Big Al"]
    assert parse_member_aliases("") == []


def test_upsert_keeps_profile_names_current(engine):
    with Session(engine) as s:
        slack_dal.upsert_slack_users_activity(
            SlackUserCreate(
                slack_id="U1", name="bobbyt", display_name="bobbyt", real_name="Bob Tables",
                slack_team_slack_id="T1",
            ),
            session=s,
            slack_team_id_map={},
        )
        slack_dal.upsert_slack_users_activity(
            SlackUserCreate(
                slack_id="U4", name="Neo", display_name=None, real_name="Neo", slack_team_slack_id="T1"
            ),
            session=s,
            slack_team_id_map={},
        )
    with Session(engine) as s:
        u1 = s.exec(select(SlackUser).where(SlackUser.slack_id == "U1")).one()
        assert (u1.name, u1.display_name, u1.real_name) == ("bobbyt", "bobbyt", "Bob Tables")
        u4 = s.exec(select(SlackUser).where(SlackUser.slack_id == "U4")).one()
        assert (u4.display_name, u4.real_name) == (None, "Neo")


# --- migration ---------------------------------------------------------------


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
    spec = importlib.util.spec_from_file_location("profile_names_migration", matches[0])
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    assert module.revision == REVISION
    return module


@pytest.fixture
def pre_engine():
    engine = _sqlite()
    meta = MetaData()
    Table(
        "slackuser",
        meta,
        Column("id", Integer, primary_key=True),
        Column("slack_id", String, nullable=False),
        Column("name", String, nullable=False),
        schema="fcm",
    )
    meta.create_all(engine)
    with engine.begin() as conn:
        conn.exec_driver_sql(
            "INSERT INTO fcm.slackuser (id, slack_id, name) VALUES (1, 'U1', 'bobbyt')"
        )
    return engine


def _run(migration, name, engine):
    with engine.begin() as conn:
        with Operations.context(MigrationContext.configure(conn)):
            getattr(migration, name)()


def _cols(engine) -> set[str]:
    return {c["name"] for c in inspect(engine).get_columns("slackuser", schema="fcm")}


def test_revision_is_an_ancestor_of_the_single_head():
    script = ScriptDirectory(str(_versions_dir().parent))
    heads = script.get_heads()
    assert len(heads) == 1
    ancestors = {r.revision for r in script.iterate_revisions(heads[0], "base")}
    assert REVISION in ancestors


def test_upgrade_adds_nullable_columns_and_keeps_name(migration, pre_engine):
    _run(migration, "upgrade", pre_engine)
    assert {"display_name", "real_name"} <= _cols(pre_engine)
    with pre_engine.begin() as conn:
        row = conn.exec_driver_sql(
            "SELECT name, display_name, real_name FROM fcm.slackuser"
        ).one()
    assert tuple(row) == ("bobbyt", None, None)


def test_downgrade_drops_the_columns(migration, pre_engine):
    _run(migration, "upgrade", pre_engine)
    _run(migration, "downgrade", pre_engine)
    assert {"display_name", "real_name"}.isdisjoint(_cols(pre_engine))
    assert "name" in _cols(pre_engine)
