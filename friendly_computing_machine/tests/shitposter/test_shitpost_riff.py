"""Thread riffs: owner routing, first-linked-replier claim, pointer/link prompt, gate.

SQLite-backed; fakes the bolt App singleton, so its own target.
"""

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

from friendly_computing_machine.src.friendly_computing_machine.bot import (  # noqa: E402
    identity_link,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    events,
    riff,
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
    ShitposterPost,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (  # noqa: E402
    SlackChannel,
)

ADMIN = "U_ADMIN"
CHAN = "C_ONE"
TEAM = "T1"
ROOT_TS = "100.1"


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
            ShitposterChannelOptIn.__table__,
            ShitposterKillSwitch.__table__,
            ShitposterPost.__table__,
        ],
    )
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    with Session(engine) as s:
        yield s


@pytest.fixture
def env(session, monkeypatch):
    linked = {
        "U_OWNER": Mock(keycloak_iss="iss", keycloak_sub="sub-owner"),
        "U_B": Mock(keycloak_iss="iss", keycloak_sub="sub-b"),
    }
    ns = Mock()
    ns.start = Mock()
    ns.client = Mock()
    ns.client.conversations_replies.return_value = {
        "messages": [{"text": "scheduled post"}, {"text": "first reply"}]
    }
    monkeypatch.setattr(riff, "get_keycloak_identity", lambda _t, u: linked.get(u))
    monkeypatch.setattr(riff, "start_workflow", ns.start)
    monkeypatch.setattr(riff, "get_app_env", Mock(return_value="test"))
    monkeypatch.setattr(riff, "get_temporal_queue_name", Mock(return_value="q"))
    monkeypatch.setattr(identity_link, "mint_link_token", Mock(return_value="tok"))
    monkeypatch.setenv("FCM_WEB_PUBLIC_URL", "https://web.example/")
    dal.set_channel_opt_in(CHAN, True, ADMIN, TEAM, session=session)
    return ns


def _channel_id(session) -> int:
    return session.exec(
        select(SlackChannel).where(SlackChannel.slack_id == CHAN)
    ).one().id


def _post(session, trigger, session_id, owner=None, ts=ROOT_TS, thread_ts=None, parent=None):
    return dal.record_post(
        slack_channel_id=_channel_id(session),
        slack_message_ts=ts,
        persona_id=1,
        persona_revision_id=1,
        trigger=trigger,
        principal_iss="iss",
        principal_sub="svc" if trigger == "scheduled" else "sub-owner",
        principal_kind="service" if trigger == "scheduled" else "human",
        whagent_session_id=session_id,
        thread_ts=thread_ts,
        thread_owner_slack_user_id=owner,
        parent_post_id=parent,
        session=session,
    )


def _reply(user="U_OWNER", text="more cats", ts="200.2", thread=ROOT_TS):
    return {
        "type": "message",
        "channel": CHAN,
        "user": user,
        "text": text,
        "ts": ts,
        "thread_ts": thread,
        "team": TEAM,
    }


def _start_params(env):
    assert env.start.call_count == 1
    return env.start.call_args.args[1]


def test_summon_owner_riffs_in_summon_session(session, env):
    root = _post(session, "summon", "S-SUMMON", owner="U_OWNER")
    assert riff.riff_thread_reply(_reply(), env.client) is True
    p = _start_params(env)
    assert p.trigger == "riff"
    assert p.whagent_session_id == "S-SUMMON"
    assert (p.principal_iss, p.principal_sub) == ("iss", "sub-owner")
    assert p.topic == "more cats"
    assert p.thread_ts == ROOT_TS
    assert p.parent_post_id == root.id
    assert p.thread_owner_slack_user_id == "U_OWNER"
    assert p.thread_context is None
    env.client.chat_postEphemeral.assert_not_called()


def test_summon_non_owner_linked_gets_pointer(session, env):
    _post(session, "summon", "S-SUMMON", owner="U_OWNER")
    assert riff.riff_thread_reply(_reply(user="U_B"), env.client) is True
    env.start.assert_not_called()
    kwargs = env.client.chat_postEphemeral.call_args.kwargs
    assert kwargs["user"] == "U_B" and kwargs["thread_ts"] == ROOT_TS
    assert "/shitpost" in kwargs["text"]


def test_summon_unlinked_non_owner_gets_link_prompt(session, env):
    _post(session, "summon", "S-SUMMON", owner="U_OWNER")
    assert riff.riff_thread_reply(_reply(user="U_NEW"), env.client) is True
    env.start.assert_not_called()
    text = env.client.chat_postEphemeral.call_args.kwargs["text"]
    assert "https://web.example/link/tok" in text


@pytest.mark.parametrize(
    "extra",
    [{"bot_id": "B1"}, {"subtype": "bot_message"}, {"subtype": "message_changed"}],
)
def test_bot_authored_or_edited_messages_trigger_nothing(session, env, extra):
    _post(session, "summon", "S-SUMMON", owner="U_OWNER")
    ev = _reply()
    ev.update(extra)
    assert riff.riff_thread_reply(ev, env.client) is False
    env.start.assert_not_called()
    env.client.chat_postEphemeral.assert_not_called()


def test_scheduled_first_linked_replier_claims_new_session(session, env):
    _post(session, "scheduled", "S-SVC")
    assert riff.riff_thread_reply(_reply(user="U_OWNER"), env.client) is True
    p = _start_params(env)
    assert p.whagent_session_id is None  # new session, never the service one
    assert (p.principal_iss, p.principal_sub) == ("iss", "sub-owner")
    assert p.thread_context == "scheduled post\nfirst reply"
    assert p.thread_owner_slack_user_id == "U_OWNER"
    owner = session.exec(select(ShitposterPost).where(ShitposterPost.trigger == "scheduled")).one()
    assert owner.thread_owner_slack_user_id == "U_OWNER"


def test_scheduled_second_linked_replier_gets_pointer(session, env):
    _post(session, "scheduled", "S-SVC")
    riff.riff_thread_reply(_reply(user="U_OWNER"), env.client)
    assert riff.riff_thread_reply(_reply(user="U_B", ts="200.3"), env.client) is True
    assert env.start.call_count == 1
    assert env.client.chat_postEphemeral.call_args.kwargs["user"] == "U_B"


def test_scheduled_conditional_claim_has_one_winner(session, env):
    root = _post(session, "scheduled", "S-SVC")
    assert dal.set_thread_owner_if_unset(root.id, "U_OWNER", session=session) is True
    assert dal.set_thread_owner_if_unset(root.id, "U_B", session=session) is False


def test_scheduled_unlinked_first_replier_does_not_claim(session, env):
    _post(session, "scheduled", "S-SVC")
    assert riff.riff_thread_reply(_reply(user="U_NEW"), env.client) is True
    env.start.assert_not_called()
    assert "https://web.example/link/tok" in env.client.chat_postEphemeral.call_args.kwargs["text"]
    owner = session.exec(select(ShitposterPost)).one()
    assert owner.thread_owner_slack_user_id is None


def test_scheduled_owner_continues_own_riff_session(session, env):
    root = _post(session, "scheduled", "S-SVC")
    riff.riff_thread_reply(_reply(user="U_OWNER"), env.client)
    _post(
        session,
        "riff",
        "S-OWNER-RIFF",
        owner="U_OWNER",
        ts="200.5",
        thread_ts=ROOT_TS,
        parent=root.id,
    )
    assert riff.riff_thread_reply(_reply(user="U_OWNER", ts="300.1"), env.client) is True
    p = env.start.call_args_list[-1].args[1]
    assert p.whagent_session_id == "S-OWNER-RIFF"
    assert p.thread_context is None
    assert p.parent_post_id == root.id


def test_silenced_does_nothing(session, env):
    _post(session, "summon", "S-SUMMON", owner="U_OWNER")
    dal.set_kill_switch(False, ADMIN, None, session=session)
    assert riff.riff_thread_reply(_reply(), env.client) is True
    env.start.assert_not_called()
    env.client.chat_postEphemeral.assert_not_called()


def test_opted_out_does_nothing(session, env):
    _post(session, "summon", "S-SUMMON", owner="U_OWNER")
    dal.set_channel_opt_in(CHAN, False, ADMIN, TEAM, session=session)
    assert riff.riff_thread_reply(_reply(), env.client) is True
    env.start.assert_not_called()
    env.client.chat_postEphemeral.assert_not_called()


def test_non_shitposter_thread_is_not_ours(session, env):
    _post(session, "summon", "S-SUMMON", owner="U_OWNER")
    assert riff.riff_thread_reply(_reply(thread="999.9"), env.client) is False
    assert riff.riff_thread_reply(_reply(thread=None), env.client) is False
    env.start.assert_not_called()
    env.client.chat_postEphemeral.assert_not_called()


@pytest.fixture
def events_env(monkeypatch):
    ns = Mock()
    ns.relay = Mock(return_value=False)
    ns.riff = Mock(return_value=False)
    ns.say = Mock()
    ns.client = Mock()
    monkeypatch.setattr(events, "relay_thread_reply", ns.relay)
    monkeypatch.setattr(events, "riff_thread_reply", ns.riff)
    monkeypatch.setattr(events, "get_bot_config", Mock(return_value=Mock(music_poll_infos=[])))
    monkeypatch.setattr(
        events,
        "SlackMessageCreate",
        Mock(
            from_slack_message_json=Mock(
                return_value=Mock(
                    slack_id="1", slack_channel_slack_id="C_OTHER", slack_user_slack_id="U1"
                )
            )
        ),
    )
    return ns


def test_handle_message_dispatches_riff_when_not_relayed(events_env):
    ev = _reply()
    events.handle_message(ev, events_env.say, events_env.client)
    events_env.riff.assert_called_once_with(ev, events_env.client)


def test_handle_message_skips_riff_when_relayed(events_env):
    events_env.relay.return_value = True
    events.handle_message(_reply(), events_env.say, events_env.client)
    events_env.riff.assert_not_called()
