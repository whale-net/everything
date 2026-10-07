"""Daily backfill over opted-in channels: missed messages and reactions.

Red-proof: dropping the opt-in union in _run turns the opted-in tests red;
passing reaction_state for every channel turns the music-poll-untouched test
red; dropping the removed_by_backfill stamp turns the stale-reaction test red.
"""

from unittest.mock import Mock, patch

from slack_sdk.errors import SlackApiError
from sqlmodel import select

from friendly_computing_machine.src.friendly_computing_machine.bot.task import musicpoll
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    add_reaction,
    set_channel_opt_in,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackMessage,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack_reaction import (
    SlackReaction,
)

from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
)

POLL_CHANNEL = "C_POLL"
TEAM = "T_TEAM"
CHAN = "C_SHIT"
OTHER = "C_OTHER"
TS = "1700000000.000100"


class _Client:
    team_id = TEAM

    def __init__(self, by_channel):
        self.by_channel = by_channel
        self.calls = []

    def conversations_history(self, channel, latest=None, oldest=None):
        self.calls.append(channel)
        # only the first page carries messages
        return {"messages": [] if latest else self.by_channel.get(channel, [])}

    def conversations_replies(self, channel, ts):
        return {"messages": []}


def _m(ts=TS, reactions=None):
    m = {"type": "message", "user": "U_A", "ts": ts, "text": "hi", "client_msg_id": "c" + ts}
    if reactions is not None:
        m["reactions"] = reactions
    return m


def _run(client):
    task = object.__new__(musicpoll.MusicPollArchiveMessages)
    # the maintenance SQL is Postgres-only; the sqlite store can't run it
    jobs = [
        patch.object(musicpoll, name)
        for name in (
            "delete_slack_message_duplicates",
            "backfill_slack_messages_slack_channel_id",
            "backfill_slack_messages_slack_user_id",
            "backfill_slack_messages_slack_team_id",
        )
    ]
    for j in jobs:
        j.start()
    try:
        with patch.object(musicpoll, "get_slack_web_client", return_value=client):
            task._run()
    finally:
        for j in jobs:
            j.stop()


def _rows(db, model):
    db.expire_all()
    return list(db.exec(select(model).order_by(model.id)).all())


def _add_channel(db, slack_id):
    db.add(SlackChannel(slack_id=slack_id, name=slack_id, channel_type="public"))
    db.commit()


def _opt_in(db):
    _add_channel(db, CHAN)
    set_channel_opt_in(CHAN, True, "U_ADMIN", TEAM, session=db)


def test_missed_message_and_reaction_backfilled_and_idempotent(slack_db):
    _opt_in(slack_db)
    client = _Client(
        {CHAN: [_m(reactions=[{"name": "fire", "users": ["U_B", "U_C"], "count": 2}])]}
    )
    _run(client)
    assert len(_rows(slack_db, SlackMessage)) == 1
    reactions = _rows(slack_db, SlackReaction)
    assert len(reactions) == 2
    assert all(r.added_by_backfill and r.added_at is None for r in reactions)

    _run(client)
    assert len(_rows(slack_db, SlackMessage)) == 1
    assert len(_rows(slack_db, SlackReaction)) == 2


def test_stale_active_reaction_removed_by_backfill(slack_db):
    _opt_in(slack_db)
    add_reaction(CHAN, TS, "U_B", "fire", session=slack_db)
    add_reaction(CHAN, TS, "U_C", "fire", session=slack_db)
    _run(_Client({CHAN: [_m(reactions=[{"name": "fire", "users": ["U_C"], "count": 1}])]}))
    by_user = {r.slack_user_slack_id: r for r in _rows(slack_db, SlackReaction)}
    assert by_user["U_B"].removed_at is not None and by_user["U_B"].removed_by_backfill
    assert by_user["U_C"].removed_at is None
    assert not by_user["U_C"].added_by_backfill


def test_music_poll_channel_messages_only_no_reactions(slack_db, poll_channel):
    client = _Client(
        {POLL_CHANNEL: [_m(reactions=[{"name": "fire", "users": ["U_B"], "count": 1}])]}
    )
    _run(client)
    assert len(_rows(slack_db, SlackMessage)) == 1
    assert _rows(slack_db, SlackReaction) == []


def test_unrelated_channel_untouched(slack_db):
    _opt_in(slack_db)
    _add_channel(slack_db, OTHER)
    client = _Client({OTHER: [_m()]})
    _run(client)
    assert OTHER not in client.calls
    assert _rows(slack_db, SlackMessage) == []


def test_opt_out_stops_archiving(slack_db):
    _opt_in(slack_db)
    set_channel_opt_in(CHAN, False, "U_ADMIN", TEAM, session=slack_db)
    client = _Client({CHAN: [_m()]})
    _run(client)
    assert client.calls == []


def test_missing_scope_logs_once_and_messages_still_stored(slack_db, caplog):
    _opt_in(slack_db)
    msgs = [
        _m("1700000000.000100", [{"name": "a", "users": ["U_B"], "count": 1}]),
        _m("1700000001.000100", [{"name": "a", "users": ["U_B"], "count": 1}]),
    ]
    err = SlackApiError("x", {"error": "missing_scope"})
    with patch.object(musicpoll, "reconcile_message_reactions", side_effect=err):
        _run(_Client({CHAN: msgs}))
    assert len(_rows(slack_db, SlackMessage)) == 2
    assert sum("reactions:read" in r.message for r in caplog.records) == 1
