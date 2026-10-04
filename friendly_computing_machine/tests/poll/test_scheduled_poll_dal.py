"""The scheduled music-poll run history and channel list (krill M5).

Two bolt-free pieces the Temporal music-poll worker reads: the
musicpoll -> slackchannel join that says which channels run weekly
polls, and the scheduledpollrun / scheduledpollrunoption tables
that keep each run's picks queryable after its poll closes.
"""

import datetime

import pytest
from sqlalchemy import event
from sqlalchemy.exc import IntegrityError
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_dal import (
    get_music_poll_channels,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
)
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
    PollOption,
)
from friendly_computing_machine.src.friendly_computing_machine.models.scheduled_poll import (
    ScheduledPollRun,
    ScheduledPollRunOption,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
)


@pytest.fixture
def session():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    tables = [
        SlackChannel.__table__,
        MusicPoll.__table__,
        Poll.__table__,
        PollOption.__table__,
        ScheduledPollRun.__table__,
        ScheduledPollRunOption.__table__,
    ]
    Base.metadata.create_all(engine, tables=tables)
    with Session(engine) as s:
        yield s


def _channel(session, slack_id: str, name: str) -> SlackChannel:
    channel = SlackChannel(slack_id=slack_id, name=name, channel_type="public_channel")
    session.add(channel)
    session.commit()
    session.refresh(channel)
    return channel


def _music_poll(session, channel: SlackChannel, name: str) -> MusicPoll:
    poll = MusicPoll(
        slack_channel_id=channel.id,
        start_date=datetime.datetime(2026, 10, 5),
        name=name,
    )
    session.add(poll)
    session.commit()
    session.refresh(poll)
    return poll


# --- the bolt-free channel list ------------------------------------------


def test_channel_list_joins_each_music_poll_to_its_channel(session):
    music = _channel(session, "C_MUSIC", "music-polls")
    other = _channel(session, "C_OTHER", "general")
    first = _music_poll(session, music, "Weekly throwback")
    second = _music_poll(session, music, "Weekly throwback 2")
    third = _music_poll(session, other, "Another channel's poll")

    channels = get_music_poll_channels(session=session)

    # every music poll, joined to its own channel -- the same
    # listing the bot config's poll infos build
    assert sorted(
        (row.music_poll.id, row.slack_channel.slack_id) for row in channels
    ) == [
        (first.id, "C_MUSIC"),
        (second.id, "C_MUSIC"),
        (third.id, "C_OTHER"),
    ]


def test_channel_list_is_empty_without_music_polls(session):
    _channel(session, "C_MUSIC", "music-polls")

    assert get_music_poll_channels(session=session) == []


# --- run history shape ----------------------------------------------------


def _run(session, **kwargs) -> ScheduledPollRun:
    defaults = dict(
        run_identity="2026-10-05T00:00:00+00:00",
        slack_channel_slack_id="C_MUSIC",
        run_at=datetime.datetime(2026, 10, 5, 0, 0, 1),
    )
    defaults.update(kwargs)
    run = ScheduledPollRun(**defaults)
    session.add(run)
    session.commit()
    session.refresh(run)
    return run


def test_run_identity_is_unique_per_channel(session):
    _run(session)

    with pytest.raises(IntegrityError):
        _run(session)


def test_same_run_identity_may_run_in_another_channel(session):
    first = _run(session)

    other = _run(session, slack_channel_slack_id="C_OTHER")

    assert other.id != first.id
    assert other.slack_channel_slack_id == "C_OTHER"


def test_option_position_is_unique_per_run(session):
    run = _run(session)
    session.add(
        ScheduledPollRunOption(
            scheduled_poll_run_id=run.id,
            position=0,
            song_identity="spotify:7xGfFoTp4y6Q9jF9bZz0Y",
            song_link="https://open.spotify.com/track/7xGfFoTp4y6Q9jF9bZz0Y",
            submitter_slack_user_slack_id="U1",
            submission_date=datetime.datetime(2026, 9, 28),
        )
    )
    session.commit()

    session.add(
        ScheduledPollRunOption(
            scheduled_poll_run_id=run.id,
            position=0,
            song_identity="another-song",
            song_link="https://example.com/another",
            submitter_slack_user_slack_id="U2",
            submission_date=datetime.datetime(2026, 9, 29),
        )
    )
    with pytest.raises(IntegrityError):
        session.commit()


def test_run_history_stays_queryable_after_the_poll_closes(session):
    """NFR 4da75133: a run's channel, identity, options and their
    vote counts remain queryable once the poll is closed."""
    poll = Poll(
        question="Weekly throwback",
        slack_channel_slack_id="C_MUSIC",
        creator_slack_user_slack_id="U0",
        automated=True,
    )
    session.add(poll)
    session.flush()
    for position, text in enumerate(["Tacos", "Pizza"]):
        session.add(PollOption(poll_id=poll.id, position=position, text=text))
    session.commit()

    run = _run(
        session,
        scheduled_fire_time=datetime.datetime(2026, 10, 5),
        poll_id=poll.id,
        slack_message_ts="123.456",
    )
    option = session.exec(
        select(PollOption).where(PollOption.poll_id == poll.id)
    ).first()
    session.add(
        ScheduledPollRunOption(
            scheduled_poll_run_id=run.id,
            poll_option_id=option.id,
            position=0,
            song_identity="spotify:7xGfFoTp4y6Q9jF9bZz0Y",
            song_link="https://open.spotify.com/track/7xGfFoTp4y6Q9jF9bZz0Y",
            submitter_slack_user_slack_id="U1",
            submission_date=datetime.datetime(2026, 9, 28, 12, 30),
        )
    )
    session.commit()

    stored = session.get(ScheduledPollRun, run.id)
    assert stored.run_identity == "2026-10-05T00:00:00+00:00"
    assert stored.slack_channel_slack_id == "C_MUSIC"
    assert stored.scheduled_fire_time == datetime.datetime(2026, 10, 5)
    assert stored.slack_message_ts == "123.456"
    assert stored.poll_id == poll.id

    rows = session.exec(
        select(ScheduledPollRunOption).where(
            ScheduledPollRunOption.scheduled_poll_run_id == run.id
        )
    ).all()
    assert len(rows) == 1
    assert rows[0].song_identity == "spotify:7xGfFoTp4y6Q9jF9bZz0Y"
    assert rows[0].poll_option_id == option.id
    assert rows[0].position == 0

    # the referenced poll option still resolves, so its per-option
    # vote counts stay readable after the poll closes
    assert session.get(PollOption, rows[0].poll_option_id).text == "Tacos"
