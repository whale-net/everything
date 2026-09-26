"""Characterization of the bot config's music-poll infos.

The config blob every listener thread reads is built from the musicpoll ->
slackchannel join and cached for a minute. Asserts that the built value tracks
the database and that concurrent readers on a cold cache all see the same
fully-populated blob, never a half-built one.
"""

import datetime
from concurrent.futures import ThreadPoolExecutor
from threading import Barrier

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    get_bot_config,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    __GLOBALS as APP_GLOBALS,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import __GLOBALS
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackUser,
)

TABLES = [SlackChannel.__table__, SlackUser.__table__, MusicPoll.__table__]


@pytest.fixture
def session():
    """In-memory db wired in as the DAL's engine, and a cold bot config cache."""
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    assert __GLOBALS["engine"] is None, "engine singleton leaked from another test"
    __GLOBALS["engine"] = engine
    APP_GLOBALS.pop("bot_config", None)
    try:
        with Session(engine) as s:
            yield s
    finally:
        APP_GLOBALS.pop("bot_config", None)
        __GLOBALS["engine"] = None


def _channel(session, slack_id, name):
    channel = SlackChannel(slack_id=slack_id, name=name, channel_type="public_channel")
    session.add(channel)
    session.commit()
    session.refresh(channel)
    return channel


def _poll(session, channel, name):
    poll = MusicPoll(
        slack_channel_id=channel.id,
        start_date=datetime.datetime(2024, 11, 30),
        name=name,
    )
    session.add(poll)
    session.commit()
    session.refresh(poll)
    return poll


def _bot_user(session, slack_id="U_BOT"):
    user = SlackUser(slack_id=slack_id, name="fcm", is_bot=True)
    session.add(user)
    session.commit()
    return user


def _pairs(config):
    return sorted(
        (info.music_poll.name, info.slack_channel.slack_id)
        for info in config.music_poll_infos
    )


def test_cold_cache_music_poll_infos_match_the_join(session):
    jams = _channel(session, "C1", "cat-jams")
    other = _channel(session, "C2", "random")
    _poll(session, jams, "weekly jams")
    _poll(session, other, "random poll")
    _bot_user(session)

    config = get_bot_config()

    assert _pairs(config) == [("random poll", "C2"), ("weekly jams", "C1")]
    assert config.BOT_SLACK_USER_IDS == {"U_BOT"}
    assert config.as_of is not None


def test_cold_cache_with_no_music_polls_is_an_empty_list(session):
    _bot_user(session)

    assert get_bot_config().music_poll_infos == []


def test_config_is_cached_until_the_refresh_period_lapses(session):
    jams = _channel(session, "C1", "cat-jams")
    _poll(session, jams, "weekly jams")
    _bot_user(session)
    assert _pairs(get_bot_config()) == [("weekly jams", "C1")]

    _poll(session, jams, "second poll")
    # still cached while the refresh period is unexpired
    assert _pairs(get_bot_config()) == [("weekly jams", "C1")]

    assert _pairs(get_bot_config(should_ignore_cache=True)) == [
        ("second poll", "C1"),
        ("weekly jams", "C1"),
    ]


def test_concurrent_cold_cache_readers_all_see_the_fully_populated_config(session):
    jams = _channel(session, "C1", "cat-jams")
    _poll(session, jams, "weekly jams")
    _poll(session, jams, "another poll")
    _bot_user(session)
    readers = 8
    barrier = Barrier(readers)

    def read(_):
        barrier.wait()
        config = get_bot_config()
        return _pairs(config), sorted(config.BOT_SLACK_USER_IDS)

    with ThreadPoolExecutor(max_workers=readers) as pool:
        observed = list(pool.map(read, range(readers)))

    assert observed == [
        ([("another poll", "C1"), ("weekly jams", "C1")], ["U_BOT"])
    ] * readers
