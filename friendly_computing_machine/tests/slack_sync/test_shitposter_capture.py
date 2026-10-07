"""Live capture in Shitposter-opted-in channels: messages and reactions.

Red-proof: dropping the opt-in clause of the store gate turns the stored-message
test red; dropping the active-row check in add_reaction turns the redelivery
test red (or trips the unique index); removing the opt-in gate in
_capture_reaction turns the non-opted-in reaction test red.
"""

from unittest.mock import Mock

from sqlmodel import select

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import events
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    list_active_reactions,
    set_channel_opt_in,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackMessage,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack_reaction import (
    SlackReaction,
)

TEAM = "T_TEAM"
CHAN = "C_SHIT"
OTHER = "C_OTHER"
TS = "1700000000.000100"


def _msg(channel, ts=TS, **extra):
    return {
        "type": "message",
        "channel": channel,
        "user": "U_A",
        "ts": ts,
        "text": "hi",
        "team": TEAM,
        **extra,
    }


def _react(channel, user, emoji="fire", ts=TS, item_type="message", event_ts="1700000100.0"):
    return {
        "type": "reaction",
        "user": user,
        "reaction": emoji,
        "item": {"type": item_type, "channel": channel, "ts": ts},
        "event_ts": event_ts,
    }


def _opt_in(db, channel=CHAN, state=True):
    set_channel_opt_in(channel, state, "U_ADMIN", TEAM, session=db)


def _messages(db):
    return list(db.exec(select(SlackMessage)).all())


def _all_reactions(db):
    db.expire_all()
    return list(db.exec(select(SlackReaction).order_by(SlackReaction.id)).all())


def test_opted_in_message_stored_once_on_redelivery(slack_db):
    _opt_in(slack_db)
    for _ in range(2):
        events.handle_message(_msg(CHAN, client_msg_id="cm1"), Mock())
    rows = _messages(slack_db)
    assert len(rows) == 1 and rows[0].slack_channel_slack_id == CHAN


def test_bot_message_and_thread_reply_stored(slack_db):
    _opt_in(slack_db)
    events.handle_message(_msg(CHAN, ts="1700000001.000100", bot_id="B1"), Mock())
    events.handle_message(
        _msg(CHAN, ts="1700000002.000100", thread_ts="1700000001.000100"), Mock()
    )
    rows = sorted(_messages(slack_db), key=lambda r: r.ts)
    assert len(rows) == 2
    assert rows[0].thread_ts is None and rows[1].thread_ts is not None


def test_unconfigured_and_opted_out_channels_store_nothing(slack_db):
    _opt_in(slack_db)
    events.handle_message(_msg(OTHER), Mock())
    assert _messages(slack_db) == []

    _opt_in(slack_db, state=False)
    events.handle_message(_msg(CHAN), Mock())
    assert _messages(slack_db) == []


def test_reaction_add_remove_readd(slack_db):
    _opt_in(slack_db)
    events.handle_reaction_added(_react(CHAN, "U_B"))
    (row,) = _all_reactions(slack_db)
    assert row.added_at is not None and row.removed_at is None

    events.handle_reaction_removed(_react(CHAN, "U_B", event_ts="1700000200.0"))
    (row,) = _all_reactions(slack_db)
    assert row.removed_at is not None
    assert list_active_reactions(CHAN, TS, session=slack_db) == []

    events.handle_reaction_added(_react(CHAN, "U_B", event_ts="1700000300.0"))
    rows = _all_reactions(slack_db)
    assert len(rows) == 2
    assert [r.removed_at is None for r in rows] == [False, True]


def test_reaction_redelivery_and_distinct_users(slack_db):
    _opt_in(slack_db)
    for _ in range(2):
        events.handle_reaction_added(_react(CHAN, "U_B"))
    assert len(list_active_reactions(CHAN, TS, session=slack_db)) == 1

    events.handle_reaction_added(_react(CHAN, "U_C"))
    active = list_active_reactions(CHAN, TS, session=slack_db)
    assert {r.slack_user_slack_id for r in active if r.emoji == "fire"} == {"U_B", "U_C"}


def test_remove_without_active_row_is_noop(slack_db):
    _opt_in(slack_db)
    events.handle_reaction_removed(_react(CHAN, "U_B"))
    assert _all_reactions(slack_db) == []


def test_skin_tone_normalized(slack_db):
    _opt_in(slack_db)
    events.handle_reaction_added(_react(CHAN, "U_B", emoji="wave::skin-tone-3"))
    events.handle_reaction_added(_react(CHAN, "U_B", emoji="wave"))
    (row,) = _all_reactions(slack_db)
    assert row.emoji == "wave"


def test_reactions_ignored_outside_opted_in_message_items(slack_db):
    events.handle_reaction_added(_react(OTHER, "U_B"))
    _opt_in(slack_db)
    events.handle_reaction_added(_react(OTHER, "U_B"))
    events.handle_reaction_added(_react(CHAN, "U_B", item_type="file"))
    _opt_in(slack_db, state=False)
    events.handle_reaction_added(_react(CHAN, "U_B"))
    assert _all_reactions(slack_db) == []


def test_reaction_links_stored_message(slack_db):
    _opt_in(slack_db)
    events.handle_message(_msg(CHAN), Mock())
    events.handle_reaction_added(_react(CHAN, "U_B"))
    (row,) = _all_reactions(slack_db)
    assert row.slack_message_id == _messages(slack_db)[0].id
