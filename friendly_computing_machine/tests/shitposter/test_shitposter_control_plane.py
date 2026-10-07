"""Shitposter control plane: SCD2 opt-in, kill switch, gate, and /shitpost admin.

Red-proof. Each mutation was applied, run, and reverted:

  * set_channel_opt_in skips the same-state check   -> no-op test, 4-row history
  * valid_to never closed on the old row            -> exactly-one-current,
                                                       and the unique index trips
  * gate reads through get_bot_config               -> the no-cache test (patched
                                                       to raise)
  * admin check removed from handle_shitpost        -> non-admin tests
  * notice returned on a no-op re-set               -> notice-on-change test
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

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    shitposter as handler,
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
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (  # noqa: E402
    parse_admin_slack_user_ids,
)

ADMINS = frozenset({"U_ADMIN"})
CHAN = "C_ONE"


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


@pytest.fixture(autouse=True)
def _reset_notifier():
    yield
    dal.set_state_change_notifier(None)


def _cmd(text, user="U_ADMIN", channel=CHAN):
    return {"user_id": user, "channel_id": channel, "text": text, "team_id": "T1"}


def _run(text, user="U_ADMIN", channel=CHAN):
    return handler.handle_shitpost(_cmd(text, user, channel), ADMINS)


def test_opt_in_out_in_yields_three_rows_one_current(session):
    for state, actor in ((True, "U1"), (False, "U2"), (True, "U3")):
        assert dal.set_channel_opt_in(CHAN, state, actor, "T1", session=session)

    history = dal.list_channel_opt_in_history(CHAN, session=session)
    assert [h.opted_in for h in history] == [True, False, True]
    assert [h.set_by_slack_user_id for h in history] == ["U1", "U2", "U3"]
    assert [h.valid_to is None for h in history] == [False, False, True]
    assert all(h.valid_from is not None for h in history)
    assert dal.is_channel_opted_in(CHAN, session=session) is True


def test_same_state_reset_writes_nothing(session):
    assert dal.set_channel_opt_in(CHAN, True, "U1", session=session)
    assert not dal.set_channel_opt_in(CHAN, True, "U2", session=session)
    # opting out of a never-opted-in channel is also a no-op
    assert not dal.set_channel_opt_in("C_NEW", False, "U2", session=session)
    assert len(dal.list_channel_opt_in_history(CHAN, session=session)) == 1
    assert dal.list_channel_opt_in_history("C_NEW", session=session) == []

    assert dal.set_kill_switch(False, "U1", "noise", session=session)
    assert not dal.set_kill_switch(False, "U2", session=session)
    assert dal.set_kill_switch(True, "U1", session=session)
    assert not dal.set_kill_switch(True, "U1", session=session)
    assert len(dal.list_kill_switch_history(session=session)) == 2


def test_kill_switch_history_and_default_on(session):
    assert dal.is_shitposter_enabled(session=session) is True
    dal.set_kill_switch(False, "operator:db", "incident", session=session)
    assert dal.is_shitposter_enabled(session=session) is False
    dal.set_kill_switch(True, "U1", session=session)
    history = dal.list_kill_switch_history(session=session)
    assert [(h.enabled, h.set_by) for h in history] == [
        (False, "operator:db"),
        (True, "U1"),
    ]
    assert history[0].reason == "incident"
    assert sum(h.valid_to is None for h in history) == 1


def test_non_admin_is_refused_and_nothing_written(session):
    for text in (
        "admin optin",
        "admin optout",
        "admin silence spam",
        "admin resume",
        "admin status",
    ):
        reply, notice = _run(text, user="U_RANDOM")
        assert "admin" in reply.lower()
        assert notice is None
    assert session.exec(select(ShitposterChannelOptIn)).all() == []
    assert session.exec(select(ShitposterKillSwitch)).all() == []


def test_empty_admin_set_refuses_everyone(session):
    assert parse_admin_slack_user_ids("") == frozenset()
    assert parse_admin_slack_user_ids(" U1 , ,U2 ") == frozenset({"U1", "U2"})
    reply, notice = handler.handle_shitpost(_cmd("admin optin"), frozenset())
    assert notice is None
    assert session.exec(select(ShitposterChannelOptIn)).all() == []


def test_non_admin_text_is_the_summon_hook(session):
    reply, notice = _run("tacos", user="U_RANDOM")
    assert "not available" in reply
    assert notice is None


def test_notice_only_on_state_change(session):
    reply, notice = _run("admin optin")
    assert notice and "captured" in notice
    assert _run("admin optin") == ("Shitposter is already on in this channel.", None)
    _, notice = _run("admin optout")
    assert notice and "off" in notice
    assert _run("admin optout")[1] is None


def test_silence_resume_status(session):
    reply, notice = _run("admin silence too loud")
    assert "silenced" in reply and notice is None
    assert dal.list_kill_switch_history(session=session)[0].reason == "too loud"
    assert "silenced" in _run("admin status")[0]
    assert "resumed" in _run("admin resume")[0]
    _run("admin optin")
    assert "Workspace: on. This channel: on" in _run("admin status")[0]


def test_command_handler_acks_records_and_says_notice(session):
    ack, respond, say = Mock(), Mock(), Mock()
    handler._ADMIN_SLACK_USER_IDS = ADMINS
    handler.handle_shitpost_command(ack, respond, say, _cmd("admin optin"))
    ack.assert_called_once()
    respond.assert_called_once()
    assert respond.call_args.kwargs["response_type"] == "ephemeral"
    say.assert_called_once()  # visible, non-ephemeral
    assert len(session.exec(select(SlackCommand)).all()) == 1

    say.reset_mock()
    handler.handle_shitpost_command(Mock(), Mock(), say, _cmd("admin optin"))
    say.assert_not_called()


def test_gate_reads_current_state_without_the_bot_config_cache(session, monkeypatch):
    def _boom(*a, **k):
        raise AssertionError("gate must not consult cached bot config")

    monkeypatch.setattr(app_mod, "get_bot_config", _boom)

    assert dal.shitposter_gate(CHAN, session=session) == dal.GateResult(
        False, "not_opted_in"
    )
    dal.set_channel_opt_in(CHAN, True, "U1", session=session)
    assert dal.shitposter_gate(CHAN, session=session).allowed is True
    dal.set_kill_switch(False, "U1", session=session)
    assert dal.shitposter_gate(CHAN, session=session) == dal.GateResult(
        False, "silenced"
    )
    dal.set_kill_switch(True, "U1", session=session)
    dal.set_channel_opt_in(CHAN, False, "U1", session=session)
    assert dal.shitposter_gate(CHAN, session=session).reason == "not_opted_in"


def test_notifier_fires_on_optin_optout_resume_only(session):
    events = []
    dal.set_state_change_notifier(lambda e, c: events.append((e, c)))
    dal.set_channel_opt_in(CHAN, True, "U1", session=session)
    dal.set_channel_opt_in(CHAN, True, "U1", session=session)
    dal.set_channel_opt_in(CHAN, False, "U1", session=session)
    dal.set_kill_switch(False, "U1", session=session)
    dal.set_kill_switch(True, "U1", session=session)
    assert events == [("optin", CHAN), ("optout", CHAN), ("resume", None)]
