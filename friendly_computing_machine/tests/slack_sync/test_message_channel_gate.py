"""The message event handler's channel gate is DB-driven (req dea2e4ba #1).

The handler stores a message only when the message's channel is one of the bot
config's music-poll infos, which SlackBotConfig builds from a musicpoll ->
slackchannel join and caches for one minute. Nothing about the gate is a
hardcoded channel list: a music poll row naming a channel is what opens the
gate for it, and a channel with no such row is never stored -- including a
channel carrying a slackspecialchannel route row, because the route tables and
the poll channel table are independent.
"""

import datetime
from unittest.mock import Mock

from sqlmodel import select

from friendly_computing_machine.src.friendly_computing_machine.bot import app as app_mod
from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    get_bot_config,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import events
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
)

TEAM = "T_TEAM"
USER = "U_SOMEONE"


def _event(channel: str, ts: str = "1700000000.000100"):
    return {
        "type": "message",
        "channel": channel,
        "user": USER,
        "ts": ts,
        "text": "hello",
        "team": TEAM,
        "client_msg_id": f"cmid-{ts}-{channel}",
    }


def _stored(session):
    return list(session.exec(select(SlackMessage)).all())


def _gated_channels():
    return {info.slack_channel.slack_id for info in get_bot_config().music_poll_infos}


def _expire_bot_config_cache():
    """The poll-channel set is cached for a minute; drop it so the next read
    rebuilds from the store, the way the refresh period eventually would."""
    app_mod.__GLOBALS.pop("bot_config", None)


def test_unconfigured_channel_stores_nothing(slack_db):
    """No music poll anywhere in the store -> the gate stores nothing."""
    events.handle_message(_event("C_NEVER_CONFIGURED"), Mock())

    assert _stored(slack_db) == []


def test_message_in_music_poll_channel_is_stored(slack_db, poll_channel):
    events.handle_message(_event(poll_channel.slack_id), Mock())

    rows = _stored(slack_db)
    assert [r.slack_channel_slack_id for r in rows] == [poll_channel.slack_id]
    assert rows[0].slack_user_slack_id == USER


def test_gate_follows_the_store_not_a_hardcoded_list(slack_db):
    """A channel id no code mentions becomes gated the moment a poll row names
    it -- that is the whole content of "the gate is DB-driven"."""
    row = SlackChannel(slack_id="C_INVENTED", name="invented", channel_type="public")
    slack_db.add(row)
    slack_db.commit()
    slack_db.refresh(row)

    events.handle_message(_event("C_INVENTED"), Mock())
    assert _stored(slack_db) == []
    assert "C_INVENTED" not in _gated_channels()

    slack_db.add(
        MusicPoll(
            slack_channel_id=row.id,
            start_date=datetime.datetime(2024, 1, 1),
            name="weekly",
        )
    )
    slack_db.commit()
    _expire_bot_config_cache()

    assert "C_INVENTED" in _gated_channels()
    events.handle_message(_event("C_INVENTED"), Mock())
    assert [r.slack_channel_slack_id for r in _stored(slack_db)] == ["C_INVENTED"]


def test_special_channel_route_does_not_open_the_gate(slack_db, routed_agent_channel):
    """A slackspecialchannel route row is not a music poll, so the gate stays
    shut. The two tables are independent by design; the route table's own
    requirement depends on them not being folded together."""
    assert routed_agent_channel.slack_id not in _gated_channels()

    events.handle_message(_event(routed_agent_channel.slack_id), Mock())

    assert _stored(slack_db) == []
