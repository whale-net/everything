import contextlib

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal.identity_dal import (
    complete_link,
    get_keycloak_identity,
    mint_link_token,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackKeycloakIdentity,
    SlackLinkToken,
)
from friendly_computing_machine.src.friendly_computing_machine.web.app import (
    create_app,
)
from friendly_computing_machine.src.friendly_computing_machine.web.config import WebConfig

TEAM = "T_TEAM"
ISS = "https://keycloak.example/realms/whagent"
AUTHORIZE_ENDPOINT = "https://keycloak.example/realms/whagent/protocol/openid-connect/auth"

CONFIG = WebConfig(
    public_url="https://fcm-web.example.com",
    oidc_issuer_url=ISS,
    oidc_client_id="fcm-web-client",
    oidc_client_secret="s3cret",
    session_secret="test-session-secret",
)

# recognizable values we plant in the mocked token response and then assert
# never reach the database.
ACCESS_TOKEN = "ACCESS-TOKEN-SHOULD-NOT-PERSIST"
REFRESH_TOKEN = "REFRESH-TOKEN-SHOULD-NOT-PERSIST"
ID_TOKEN = "ID-TOKEN-SHOULD-NOT-PERSIST"


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


def _build_client(session, monkeypatch, access_token_result=None, exchange_error=None):
    """Build a TestClient whose Authlib client is stubbed for offline use."""
    app = create_app(CONFIG, session_provider=lambda: contextlib.nullcontext(session))
    oauth = app.state.oauth
    client = oauth.keycloak

    # seed metadata so load_server_metadata never hits the network
    client.server_metadata["authorization_endpoint"] = AUTHORIZE_ENDPOINT
    client.server_metadata["_loaded_at"] = 0

    async def _fake_authorize_access_token(request, **kwargs):
        if exchange_error is not None:
            raise exchange_error
        return access_token_result

    monkeypatch.setattr(client, "authorize_access_token", _fake_authorize_access_token)
    return TestClient(app), client


def _token_response(iss=ISS, sub="sub-1"):
    return {
        "access_token": ACCESS_TOKEN,
        "refresh_token": REFRESH_TOKEN,
        "id_token": ID_TOKEN,
        "token_type": "Bearer",
        "userinfo": {"iss": iss, "sub": sub},
    }


def _identities(session):
    return session.exec(select(SlackKeycloakIdentity)).all()


def _all_rows_as_text(session):
    """Dump every column of every identity/link-token row as text."""
    text = []
    for row in _identities(session):
        text.extend(str(v) for v in row.model_dump().values())
    for row in session.exec(select(SlackLinkToken)).all():
        text.extend(str(v) for v in row.model_dump().values())
    return "\n".join(text)


# 1. /link/{valid} => 302 to the Keycloak authorize endpoint
def test_valid_link_redirects_to_keycloak_authorize(session, monkeypatch):
    token = mint_link_token(TEAM, "U1", session=session)
    tc, _ = _build_client(session, monkeypatch)

    resp = tc.get(f"/link/{token}", follow_redirects=False)

    assert resp.status_code == 302
    location = resp.headers["location"]
    assert location.startswith(AUTHORIZE_ENDPOINT)
    assert "client_id=fcm-web-client" in location
    assert "redirect_uri=https%3A%2F%2Ffcm-web.example.com%2Flink%2Fcallback" in location


# 2. /link/{expired|consumed|unknown} => error page, no redirect
def test_expired_link_is_rejected(session, monkeypatch):
    import datetime

    token = mint_link_token(
        TEAM, "U1", ttl=datetime.timedelta(minutes=-1), session=session
    )
    tc, _ = _build_client(session, monkeypatch)

    resp = tc.get(f"/link/{token}", follow_redirects=False)

    assert resp.status_code == 400
    assert "Location" not in resp.headers
    assert "Link failed" in resp.text


def test_consumed_link_is_rejected(session, monkeypatch):
    token = mint_link_token(TEAM, "U1", session=session)
    complete_link(token, ISS, "sub-1", session=session)
    tc, _ = _build_client(session, monkeypatch)

    resp = tc.get(f"/link/{token}", follow_redirects=False)

    assert resp.status_code == 400
    assert "Location" not in resp.headers


def test_unknown_link_is_rejected(session, monkeypatch):
    tc, _ = _build_client(session, monkeypatch)

    resp = tc.get("/link/no-such-token", follow_redirects=False)

    assert resp.status_code == 400
    assert "Location" not in resp.headers


# 3. callback with valid session token + verified claims => mapping written
def test_callback_writes_mapping_and_consumes_token(session, monkeypatch):
    token = mint_link_token(TEAM, "U1", session=session)
    tc, _ = _build_client(session, monkeypatch, access_token_result=_token_response())

    tc.get(f"/link/{token}", follow_redirects=False)
    resp = tc.get("/link/callback?code=abc&state=xyz")

    assert resp.status_code == 200
    assert "Linked" in resp.text

    stored = get_keycloak_identity(TEAM, "U1", session=session)
    assert stored is not None
    assert stored.keycloak_iss == ISS
    assert stored.keycloak_sub == "sub-1"

    row = session.exec(
        select(SlackLinkToken).where(SlackLinkToken.token == token)
    ).one()
    assert row.consumed is True


# 4. callback replay (token already consumed) => refused, mapping unchanged
def test_callback_replay_refused_mapping_unchanged(session, monkeypatch):
    token = mint_link_token(TEAM, "U1", session=session)
    tc, _ = _build_client(
        session, monkeypatch, access_token_result=_token_response(sub="sub-hijack")
    )

    # start a real link attempt so the session holds the (still-valid) token
    assert tc.get(f"/link/{token}", follow_redirects=False).status_code == 302
    # the token is spent out-of-band (e.g. a second tab/attempt won the race)
    assert complete_link(token, ISS, "sub-original", session=session) is not None

    resp = tc.get("/link/callback?code=abc&state=xyz")

    assert resp.status_code == 410
    assert "Link failed" in resp.text
    stored = get_keycloak_identity(TEAM, "U1", session=session)
    assert stored.keycloak_sub == "sub-original"
    assert len(_identities(session)) == 1


# 5. callback where the Authlib exchange raises => error page, nothing written
def test_callback_exchange_error_writes_nothing(session, monkeypatch):
    token = mint_link_token(TEAM, "U1", session=session)
    tc, _ = _build_client(
        session, monkeypatch, exchange_error=RuntimeError("user cancelled")
    )

    tc.get(f"/link/{token}", follow_redirects=False)
    resp = tc.get("/link/callback?error=access_denied&state=xyz")

    assert resp.status_code == 400
    assert "Link failed" in resp.text
    assert get_keycloak_identity(TEAM, "U1", session=session) is None
    assert _identities(session) == []
    # the abandoned attempt did not consume the token either
    row = session.exec(
        select(SlackLinkToken).where(SlackLinkToken.token == token)
    ).one()
    assert row.consumed is False


# 6. callback with no link token in session => refused
def test_callback_without_link_token_refused(session, monkeypatch):
    tc, _ = _build_client(session, monkeypatch, access_token_result=_token_response())

    resp = tc.get("/link/callback?code=abc&state=xyz")

    assert resp.status_code == 400
    assert "Link failed" in resp.text
    assert _identities(session) == []


# 7. no access/refresh/ID token is written to the DB
def test_no_keycloak_tokens_persisted(session, monkeypatch):
    token = mint_link_token(TEAM, "U1", session=session)
    tc, _ = _build_client(session, monkeypatch, access_token_result=_token_response())

    tc.get(f"/link/{token}", follow_redirects=False)
    resp = tc.get("/link/callback?code=abc&state=xyz")
    assert resp.status_code == 200

    stored = get_keycloak_identity(TEAM, "U1", session=session)
    # only iss/sub identify the Keycloak user; the mapping has no token columns
    assert stored.keycloak_iss == ISS
    assert stored.keycloak_sub == "sub-1"
    identity_columns = set(SlackKeycloakIdentity.model_fields)
    assert "access_token" not in identity_columns
    assert "refresh_token" not in identity_columns
    assert "id_token" not in identity_columns

    db_text = _all_rows_as_text(session)
    for secret in (ACCESS_TOKEN, REFRESH_TOKEN, ID_TOKEN):
        assert secret not in db_text


# 8. /health => 200
def test_health_returns_200(session, monkeypatch):
    tc, _ = _build_client(session, monkeypatch)
    resp = tc.get("/health")
    assert resp.status_code == 200
