"""Characterization of the hourly music-poll pickup chain and its window query.

Asserts the behavior the pickup has today: a poll week that yields no responses
is ordinary operation, because an instance's window has no upper bound until
its successor is posted, so the open instance matches no messages and the
pickup correctly leaves it alone. Voting is by posting a link, not by reacting.
"""

import datetime
from pathlib import Path

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.bot.task.musicpoll import (
    MusicPollProcessPoll,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_dal import (
    get_unprocessed_music_poll_instances,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_dal import (
    find_poll_instance_messages,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import __GLOBALS
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
    MusicPollInstance,
    MusicPollResponse,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.models.task import (
    TaskInstanceStatus,
)

TABLES = [
    SlackChannel.__table__,
    SlackUser.__table__,
    SlackMessage.__table__,
    MusicPoll.__table__,
    MusicPollInstance.__table__,
    MusicPollResponse.__table__,
]

# a poll window: the earlier instance opens it, its successor closes it
WINDOW_OPEN = datetime.datetime(2026, 9, 19, 12, 0, 0)
WINDOW_CLOSE = datetime.datetime(2026, 9, 26, 12, 0, 0)
ANNOUNCEMENT = ":catjam: any cat jammers? :catjam:"
SONG_URL = "https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT"


@pytest.fixture
def session():
    """In-memory db wired in as the DAL's engine, so production call sites run unchanged."""
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    assert __GLOBALS["engine"] is None, "engine singleton leaked from another test"
    __GLOBALS["engine"] = engine
    try:
        with Session(engine) as s:
            yield s
    finally:
        __GLOBALS["engine"] = None


def _channel(session, slack_id="C1", name="cat-jams"):
    channel = SlackChannel(slack_id=slack_id, name=name, channel_type="public_channel")
    session.add(channel)
    session.commit()
    session.refresh(channel)
    return channel


def _user(session, slack_id="U1"):
    user = SlackUser(slack_id=slack_id, name=slack_id, is_bot=False)
    session.add(user)
    session.commit()
    session.refresh(user)
    return user


def _message(session, channel, user, text, ts):
    message = SlackMessage(
        slack_id=None,
        slack_team_slack_id="T1",
        slack_channel_slack_id=channel.slack_id,
        slack_user_slack_id=user.slack_id,
        text=text,
        ts=ts,
        thread_ts=None,
        parent_user_slack_id=None,
        processed_date=None,
        slack_team_id=None,
        slack_channel_id=channel.id,
        slack_user_id=user.id,
        slack_parent_user_id=None,
    )
    session.add(message)
    session.commit()
    session.refresh(message)
    return message


def _poll(session, channel):
    poll = MusicPoll(
        slack_channel_id=channel.id,
        start_date=datetime.datetime(2024, 11, 30),
        name="music poll",
    )
    session.add(poll)
    session.commit()
    session.refresh(poll)
    return poll


def _instance(session, poll, channel, user, created_at, next_instance_id=None):
    announcement = _message(session, channel, user, ANNOUNCEMENT, created_at)
    instance = MusicPollInstance(
        music_poll_id=poll.id,
        slack_message_id=announcement.id,
        created_at=created_at,
        next_instance_id=next_instance_id,
    )
    session.add(instance)
    session.commit()
    session.refresh(instance)
    return instance


def _response(session, instance, channel, user, ts, url=SONG_URL):
    message = _message(session, channel, user, url, ts)
    response = MusicPollResponse(
        music_poll_instance_id=instance.id,
        slack_user_id=user.id,
        slack_message_id=message.id,
        created_at=ts,
        url=url,
    )
    session.add(response)
    session.commit()
    return response


def _run_pickup():
    """Drive the hourly pickup, skipping the task bookkeeping AbstractTask.__init__ does."""
    task = MusicPollProcessPoll.__new__(MusicPollProcessPoll)
    return task._run()


def _window(session, channel, user, poll):
    """A two-instance poll: `opened`'s window is bounded by `closing`'s creation time."""
    closing = _instance(session, poll, channel, user, WINDOW_CLOSE)
    opened = _instance(session, poll, channel, user, WINDOW_OPEN)
    opened.next_instance_id = closing.id
    session.commit()
    return opened, closing


# -----
# the window / eligibility query


def test_instance_with_a_successor_and_no_responses_is_selected(session):
    channel, user = _channel(session), _user(session)
    opened, _closing = _window(session, channel, user, _poll(session, channel))

    # the pickup is single-shot per instance: once a response row exists the
    # instance drops out and is never picked up again
    assert opened.id in [i.id for i in get_unprocessed_music_poll_instances()]


def test_instance_that_already_has_a_response_is_not_selected(session):
    channel, user = _channel(session), _user(session)
    opened, _closing = _window(session, channel, user, _poll(session, channel))
    _response(session, opened, channel, user, WINDOW_OPEN + datetime.timedelta(hours=1))

    assert opened.id not in [i.id for i in get_unprocessed_music_poll_instances()]


def test_open_instance_without_a_successor_is_also_returned(session):
    channel, user = _channel(session), _user(session)
    opened, closing = _window(session, channel, user, _poll(session, channel))

    # the successor clause reads `next_instance_id is not null()` -- a python
    # identity test against the null() sql function, so it is always true and
    # filters nothing. the newest instance is therefore returned alongside the
    # one that does have a successor.
    assert closing.next_instance_id is None
    assert [i.id for i in get_unprocessed_music_poll_instances()] == [
        closing.id,
        opened.id,
    ]


def test_open_instance_has_no_upper_bound_so_its_window_matches_nothing(session):
    channel, user = _channel(session), _user(session)
    _window(session, channel, user, _poll(session, channel))
    _message(session, channel, user, SONG_URL, WINDOW_CLOSE)

    # an instance with no successor has a NULL window upper bound, so no stored
    # message satisfies `ts < successor.created_at` and it yields no responses
    assert _run_pickup() is TaskInstanceStatus.OK
    assert session.exec(select(MusicPollResponse)).all() == []


def test_window_runs_from_own_created_at_to_successor_created_at(session):
    channel, user = _channel(session), _user(session)
    opened, closing = _window(session, channel, user, _poll(session, channel))
    before = _message(
        session, channel, user, SONG_URL, WINDOW_OPEN - datetime.timedelta(hours=1)
    )
    at_open = _message(session, channel, user, SONG_URL, WINDOW_OPEN)
    before_close = _message(
        session, channel, user, SONG_URL, WINDOW_CLOSE - datetime.timedelta(seconds=1)
    )
    at_close = _message(session, channel, user, SONG_URL, WINDOW_CLOSE)

    found = {m.id for m in find_poll_instance_messages(opened)}

    # inclusive of the opening bound, exclusive of the closing bound, so a link
    # posted at the successor's created_at belongs to the successor's window
    assert found == {
        at_open.id,
        before_close.id,
        # the instance's own announcement sits exactly on the opening bound
        opened.slack_message_id,
    }
    assert before.id not in found
    assert at_close.id not in found
    assert closing.slack_message_id not in found


# -----
# the pickup chain end to end


def test_pickup_creates_one_response_row_for_a_link_inside_the_window(session):
    channel, user = _channel(session), _user(session)
    opened, _closing = _window(session, channel, user, _poll(session, channel))
    vote = _message(
        session,
        channel,
        user,
        f"my pick {SONG_URL}",
        WINDOW_OPEN + datetime.timedelta(hours=2),
    )

    assert _run_pickup() is TaskInstanceStatus.OK

    responses = session.exec(select(MusicPollResponse)).all()
    assert len(responses) == 1
    assert responses[0].music_poll_instance_id == opened.id
    assert responses[0].slack_message_id == vote.id
    assert responses[0].slack_user_id == user.id
    assert responses[0].url == SONG_URL
    # the instance now has a response, so a second pass adds nothing
    assert _run_pickup() is TaskInstanceStatus.OK
    assert len(session.exec(select(MusicPollResponse)).all()) == 1


def test_pickup_creates_no_row_for_a_message_outside_the_window(session):
    channel, user = _channel(session), _user(session)
    _window(session, channel, user, _poll(session, channel))
    _message(
        session, channel, user, SONG_URL, WINDOW_OPEN - datetime.timedelta(hours=1)
    )
    _message(
        session, channel, user, SONG_URL, WINDOW_CLOSE + datetime.timedelta(hours=1)
    )

    assert _run_pickup() is TaskInstanceStatus.OK
    assert session.exec(select(MusicPollResponse)).all() == []


def test_pickup_creates_no_row_for_a_message_without_a_url(session):
    channel, user = _channel(session), _user(session)
    _window(session, channel, user, _poll(session, channel))
    _message(session, channel, user, "no song from me today", WINDOW_OPEN)

    assert _run_pickup() is TaskInstanceStatus.OK
    assert session.exec(select(MusicPollResponse)).all() == []


def test_pickup_ignores_messages_from_other_channels(session):
    channel, user = _channel(session), _user(session)
    other = _channel(session, slack_id="C2", name="random")
    _window(session, channel, user, _poll(session, channel))
    _message(session, other, user, SONG_URL, WINDOW_OPEN + datetime.timedelta(hours=1))

    assert _run_pickup() is TaskInstanceStatus.OK
    assert session.exec(select(MusicPollResponse)).all() == []


def test_pickup_creates_one_row_per_url_in_a_multi_link_message(session):
    channel, user = _channel(session), _user(session)
    _window(session, channel, user, _poll(session, channel))
    second = "https://youtube.com/watch?v=abc123"
    _message(
        session,
        channel,
        user,
        f"{SONG_URL} and {second}",
        WINDOW_OPEN + datetime.timedelta(hours=1),
    )

    assert _run_pickup() is TaskInstanceStatus.OK
    urls = [r.url for r in session.exec(select(MusicPollResponse)).all()]
    assert sorted(urls) == sorted([SONG_URL, second])


def test_voting_is_by_posting_a_link_not_by_reacting(session):
    import friendly_computing_machine.src.friendly_computing_machine as fcm_pkg

    # no reaction event is subscribed to or read anywhere in FCM, so a reaction
    # is never a vote; the only way to add a response row is a link in the text
    reacting = [
        str(path)
        for path in Path(fcm_pkg.__file__).parent.rglob("*.py")
        if "reaction_added" in path.read_text()
    ]
    assert reacting == []

    channel, user = _channel(session), _user(session)
    _window(session, channel, user, _poll(session, channel))
    # a bare emoji reaction leaves no url behind in the stored text
    _message(
        session, channel, user, "\U0001f408", WINDOW_OPEN + datetime.timedelta(hours=1)
    )

    assert _run_pickup() is TaskInstanceStatus.OK
    assert session.exec(select(MusicPollResponse)).all() == []
