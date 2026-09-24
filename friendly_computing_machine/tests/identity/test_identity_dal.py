import datetime
import importlib
import importlib.resources

import pytest
from alembic.migration import MigrationContext
from alembic.operations import Operations
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal.identity_dal import (
    LINK_TOKEN_TTL,
    complete_link,
    get_keycloak_identity,
    mint_link_token,
    peek_link_token,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackKeycloakIdentity,
    SlackLinkToken,
)

TEAM = "T_TEAM"
ISS = "https://keycloak.example/realms/fcm"


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
    tables = [SlackKeycloakIdentity.__table__, SlackLinkToken.__table__]
    Base.metadata.create_all(engine, tables=tables)
    with Session(engine) as s:
        yield s


def _tokens(session):
    return session.exec(select(SlackLinkToken)).all()


def _identities(session):
    return session.exec(select(SlackKeycloakIdentity)).all()


def test_mint_distinct_unguessable_tokens_with_expiry(session):
    first = mint_link_token(TEAM, "U1", session=session)
    second = mint_link_token(TEAM, "U2", session=session)

    assert first != second
    # secrets.token_urlsafe(32) -> 43 url-safe base64 chars
    assert len(first) == 43
    assert len(second) == 43

    before = datetime.datetime.now(datetime.UTC)
    ttl = datetime.timedelta(minutes=5)
    token = mint_link_token(TEAM, "U3", ttl=ttl, session=session)
    after = datetime.datetime.now(datetime.UTC)

    row = session.exec(
        select(SlackLinkToken).where(SlackLinkToken.token == token)
    ).one()
    assert row.slack_team_id == TEAM
    assert row.slack_user_id == "U3"
    # SQLite drops tzinfo on round-trip, so compare naive-UTC to naive-UTC.
    expires = row.expires_at.replace(tzinfo=datetime.UTC)
    assert before + ttl <= expires <= after + ttl
    assert row.consumed is False


def test_default_ttl_is_ten_minutes():
    assert LINK_TOKEN_TTL == datetime.timedelta(minutes=10)


def test_complete_link_writes_mapping_and_consumes_token(session):
    token = mint_link_token(TEAM, "U1", session=session)

    identity = complete_link(token, ISS, "sub-1", session=session)

    assert identity is not None
    assert identity.slack_team_id == TEAM
    assert identity.slack_user_id == "U1"
    assert identity.keycloak_iss == ISS
    assert identity.keycloak_sub == "sub-1"

    stored = get_keycloak_identity(TEAM, "U1", session=session)
    assert stored is not None
    assert stored.keycloak_sub == "sub-1"

    row = session.exec(
        select(SlackLinkToken).where(SlackLinkToken.token == token)
    ).one()
    assert row.consumed is True
    assert row.consumed_at is not None


def test_replay_returns_none_and_leaves_mapping_unchanged(session):
    token = mint_link_token(TEAM, "U1", session=session)
    assert complete_link(token, ISS, "sub-1", session=session) is not None

    replay = complete_link(token, ISS, "sub-hijack", session=session)

    assert replay is None
    stored = get_keycloak_identity(TEAM, "U1", session=session)
    assert stored.keycloak_sub == "sub-1"
    # no extra mapping rows were written by the replay
    assert len(_identities(session)) == 1


def test_expired_token_returns_none_and_writes_nothing(session):
    # mint already-expired by giving a negative TTL
    token = mint_link_token(TEAM, "U1", ttl=datetime.timedelta(minutes=-1), session=session)

    assert complete_link(token, ISS, "sub-1", session=session) is None
    assert get_keycloak_identity(TEAM, "U1", session=session) is None
    assert _identities(session) == []
    # the rejected attempt did not consume the token either
    row = session.exec(
        select(SlackLinkToken).where(SlackLinkToken.token == token)
    ).one()
    assert row.consumed is False


def test_unknown_token_returns_none(session):
    assert complete_link("no-such-token", ISS, "sub-1", session=session) is None
    assert peek_link_token("no-such-token", session=session) is None


def test_relink_overwrites_in_place(session):
    first = mint_link_token(TEAM, "U1", session=session)
    complete_link(first, ISS, "sub-1", session=session)

    second = mint_link_token(TEAM, "U1", session=session)
    updated = complete_link(second, ISS, "sub-2", session=session)

    assert updated is not None
    assert updated.keycloak_sub == "sub-2"

    rows = _identities(session)
    assert len(rows) == 1
    assert rows[0].keycloak_sub == "sub-2"


def test_complete_link_isolates_slack_users(session):
    token_a = mint_link_token(TEAM, "U1", session=session)
    token_b = mint_link_token(TEAM, "U2", session=session)

    complete_link(token_a, ISS, "sub-a", session=session)

    # completing A did not touch B
    assert get_keycloak_identity(TEAM, "U2", session=session) is None
    assert len(_identities(session)) == 1

    complete_link(token_b, ISS, "sub-b", session=session)
    assert get_keycloak_identity(TEAM, "U1", session=session).keycloak_sub == "sub-a"
    assert get_keycloak_identity(TEAM, "U2", session=session).keycloak_sub == "sub-b"


def test_peek_does_not_consume(session):
    token = mint_link_token(TEAM, "U1", session=session)

    peeked = peek_link_token(token, session=session)
    assert peeked is not None
    assert peeked.slack_user_id == "U1"
    assert peeked.consumed is False

    # peeking again still works, and the token is still redeemable
    assert peek_link_token(token, session=session) is not None
    row = session.exec(
        select(SlackLinkToken).where(SlackLinkToken.token == token)
    ).one()
    assert row.consumed is False

    assert complete_link(token, ISS, "sub-1", session=session) is not None
    # once consumed, peek no longer returns it
    assert peek_link_token(token, session=session) is None


def _migration_module():
    return importlib.import_module(
        "friendly_computing_machine.src.migrations.versions."
        "2026_09_24_1400-c1d4e8a7b2f9_slack_keycloak_identity"
    )


def _fcm_table_names(conn):
    return {
        row[0]
        for row in conn.exec_driver_sql(
            "SELECT name FROM fcm.sqlite_master WHERE type='table'"
        )
    }


def test_migration_upgrade_downgrade_round_trip():
    migration = _migration_module()
    engine = _engine()

    with engine.connect() as conn:
        assert _fcm_table_names(conn) == set()

        ctx = MigrationContext.configure(conn)
        with Operations.context(ctx):
            migration.upgrade()
        assert "slacklinktoken" in _fcm_table_names(conn)
        assert "slackkeycloakidentity" in _fcm_table_names(conn)

        ctx = MigrationContext.configure(conn)
        with Operations.context(ctx):
            migration.downgrade()
        assert _fcm_table_names(conn) == set()
