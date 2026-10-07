"""/shitpost summon: gate, identity, on-behalf-of workflow start (SQLite-backed)."""

from unittest.mock import Mock

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.bot import app as app_mod


class _FakeApp:
    """Stand-in for slack_bolt App: every decorator is a passthrough."""

    def __getattr__(self, _name):
        def _maybe(*a, **k):
            if len(a) == 1 and not k and callable(a[0]):
                return a[0]

            def deco(fn):
                return fn

            return deco

        return _maybe


app_mod._app_instance = _FakeApp()

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    shitposter as handler,
)
from friendly_computing_machine.src.friendly_computing_machine.bot import (  # noqa: E402
    identity_link,
)
from friendly_computing_machine.src.friendly_computing_machine.db import (  # noqa: E402
    util as db_util,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (  # noqa: E402
    shitposter_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base  # noqa: E402
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (  # noqa: E402
    ShitposterChannelOptIn,
    ShitposterKillSwitch,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (  # noqa: E402
    SlackChannel,
    SlackCommand,
)

ADMINS = frozenset({"U_ADMIN"})
CHAN = "C_ONE"
TEAM = "T1"


@pytest.fixture
def session(monkeypatch):
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(
        engine,
        tables=[
            SlackChannel.__table__,
            SlackCommand.__table__,
            ShitposterChannelOptIn.__table__,
            ShitposterKillSwitch.__table__,
        ],
    )
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    with Session(engine) as s:
        yield s


@pytest.fixture
def env(session, monkeypatch):
    identity = Mock(keycloak_iss="https://kc/realms/x", keycloak_sub="sub-1")
    ns = Mock()
    ns.get_identity = Mock(return_value=identity)
    ns.mint = Mock(return_value="tok123")
    ns.start = Mock()
    ns.client = Mock()
    monkeypatch.setattr(handler, "get_keycloak_identity", ns.get_identity)
    monkeypatch.setattr(identity_link, "mint_link_token", ns.mint)
    monkeypatch.setattr(handler, "start_workflow", ns.start)
    monkeypatch.setattr(handler, "get_app_env", Mock(return_value="test"))
    monkeypatch.setattr(handler, "get_temporal_queue_name", Mock(return_value="q"))
    monkeypatch.setenv("FCM_WEB_PUBLIC_URL", "https://web.example/")
    dal.set_channel_opt_in(CHAN, True, "U_ADMIN", TEAM, session=session)
    return ns


def _summon(env, text="", user="U_S"):
    return handler.handle_shitpost(
        {"user_id": user, "channel_id": CHAN, "text": text, "team_id": TEAM},
        ADMINS,
        env.client,
    )


def test_linked_user_starts_on_behalf_of_with_topic(env):
    reply, notice = _summon(env, "  cats  ")
    assert reply == handler.SUMMON_STARTED and notice is None
    env.get_identity.assert_called_once_with(TEAM, "U_S")
    params = env.start.call_args.args[1]
    assert params.trigger == "summon"
    assert (params.principal_iss, params.principal_sub) == ("https://kc/realms/x", "sub-1")
    assert params.topic == "cats"
    assert params.channel_slack_id == CHAN
    assert params.notice_slack_user_id == "U_S"
    assert params.thread_owner_slack_user_id == "U_S"
    assert params.thread_ts is None


def test_empty_topic_is_none(env):
    _summon(env, "   ")
    assert env.start.call_args.args[1].topic is None


def test_unlinked_gets_link_prompt_and_no_workflow(env):
    env.get_identity.return_value = None
    _summon(env)
    env.start.assert_not_called()
    kwargs = env.client.chat_postEphemeral.call_args.kwargs
    assert kwargs["channel"] == CHAN and kwargs["user"] == "U_S"
    assert "https://web.example/link/tok123" in kwargs["text"]


def test_not_opted_in_refused_before_identity_lookup(env, session):
    dal.set_channel_opt_in(CHAN, False, "U_ADMIN", TEAM, session=session)
    reply, _ = _summon(env)
    assert reply == handler.NOT_AVAILABLE
    env.start.assert_not_called()
    env.get_identity.assert_not_called()
    env.client.chat_postEphemeral.assert_not_called()


def test_silenced_refused(env, session):
    dal.set_kill_switch(False, "U_ADMIN", None, session=session)
    reply, _ = _summon(env)
    assert reply == handler.NOT_AVAILABLE
    env.start.assert_not_called()


def test_opt_out_then_immediate_summon_is_refused(env, session):
    assert _summon(env)[0] == handler.SUMMON_STARTED
    dal.set_channel_opt_in(CHAN, False, "U_ADMIN", TEAM, session=session)
    assert _summon(env)[0] == handler.NOT_AVAILABLE
    assert env.start.call_count == 1


def test_admin_text_does_not_summon(env):
    reply, _ = _summon(env, "admin status", user="U_ADMIN")
    assert reply.startswith("Workspace:")
    env.start.assert_not_called()
