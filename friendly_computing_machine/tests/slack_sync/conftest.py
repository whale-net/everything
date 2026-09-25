"""Shared fixtures for the Slack message-store / user-sync characterization.

Importing a handler module runs its @app.event decorator, which would build a
real Bolt App (and make a network auth.test call). We pre-seed the app module's
singleton with a fake whose decorators are passthroughs, so the handlers stay
plain callables we invoke directly. This has to happen here, at conftest import
time, which is before any test module imports the handlers.
"""

import datetime

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine

from friendly_computing_machine.src.friendly_computing_machine.bot import app as app_mod
from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackSpecialChannel,
    SlackSpecialChannelType,
    SlackTeam,
    SlackUser,
)

POLL_CHANNEL = "C_POLL"
AGENT_CHANNEL = "C_AGENT"


class _FakeApp:
    """Stand-in for slack_bolt App: every decorator is a passthrough."""

    def __getattr__(self, _name):
        def _maybe(*a, **k):
            # direct decorator use: @app.middleware -> return fn unchanged
            if len(a) == 1 and not k and callable(a[0]):
                return a[0]

            # factory use: @app.event("x") / @app.action("x") -> passthrough deco
            def deco(fn):
                return fn

            return deco

        return _maybe


app_mod._app_instance = _FakeApp()

# only the tables the characterized code paths read or write
TABLES = [
    SlackTeam.__table__,
    SlackUser.__table__,
    SlackChannel.__table__,
    SlackMessage.__table__,
    SlackSpecialChannelType.__table__,
    SlackSpecialChannel.__table__,
    MusicPoll.__table__,
]


@pytest.fixture
def slack_db(monkeypatch):
    """A real sqlite-backed store wired into the DAL's global engine.

    The message handler, the upsert, and the user sync all reach the database
    through db.util.SessionManager's engine singleton, so the characterization
    drives that singleton rather than mocking the DAL.
    """
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    # SlackBotConfig is cached in the app module's globals for a minute; None
    # makes the next get_bot_config() rebuild it from the store.
    monkeypatch.setitem(app_mod.__GLOBALS, "bot_config", None)
    with Session(engine) as s:
        yield s
    engine.dispose()


def _add_channel(session, slack_id: str):
    channel = SlackChannel(slack_id=slack_id, name=slack_id, channel_type="public")
    session.add(channel)
    session.commit()
    session.refresh(channel)
    return channel


@pytest.fixture
def poll_channel(slack_db):
    """A channel with a music poll -- i.e. one the message gate stores into."""
    channel = _add_channel(slack_db, POLL_CHANNEL)
    poll = MusicPoll(
        slack_channel_id=channel.id,
        start_date=datetime.datetime(2024, 1, 1),
        name="weekly",
    )
    slack_db.add(poll)
    slack_db.commit()
    return channel


@pytest.fixture
def routed_agent_channel(slack_db):
    """A channel with a slackspecialchannel route row but no music poll.

    The message gate reads the musicpoll -> slackchannel join, so this channel
    is exactly the "routed, but not a poll channel" case that must NOT be
    stored. The two route tables are independent; nothing here is a bug.
    """
    channel = _add_channel(slack_db, AGENT_CHANNEL)
    special_type = SlackSpecialChannelType(
        type_name="manman_dev", friendly_type_name="manman dev"
    )
    slack_db.add(special_type)
    slack_db.commit()
    slack_db.refresh(special_type)
    slack_db.add(
        SlackSpecialChannel(
            slack_channel_id=channel.id,
            slack_special_channel_type_id=special_type.id,
        )
    )
    slack_db.commit()
    return channel
