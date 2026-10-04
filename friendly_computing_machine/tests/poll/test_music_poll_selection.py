"""The weekly music-poll selection (krill M5).

Pins the song-selection the Temporal selection activity runs:
song identity normalization, earliest-submission attribution,
both exclusion rules (the two most recent closed windows in
chain order, and the last eight run-posted polls), the
submitter cap's precedence over the recent/goldie slot split,
the 4- and 3-option fills, the fewest-appearances pick with
its random tiebreak, and the skip when fewer than three songs
are pickable.
"""

import datetime
import random

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_selection import (
    SelectedOption,
    select_scheduled_poll_options,
    song_identity,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import (
    Base,
)
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
    MusicPollInstance,
    MusicPollResponse,
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
    SlackMessage,
    SlackUser,
)

TABLES = [
    SlackChannel.__table__,
    SlackUser.__table__,
    SlackMessage.__table__,
    MusicPoll.__table__,
    MusicPollInstance.__table__,
    MusicPollResponse.__table__,
    Poll.__table__,
    PollOption.__table__,
    ScheduledPollRun.__table__,
    ScheduledPollRunOption.__table__,
]

# a Monday; every window below starts on a Monday too
NOW = datetime.datetime(2026, 10, 5, tzinfo=datetime.UTC)
ANNOUNCEMENT = ":catjam: any cat jammers? :catjam:"
CHANNEL_SLACK_ID = "C_MUSIC"


@pytest.fixture
def session():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    with Session(engine) as s:
        yield s


# ----- fixtures -------------------------------------------------------


def _channel(session, slack_id=CHANNEL_SLACK_ID, name="cat-jams"):
    channel = SlackChannel(slack_id=slack_id, name=name, channel_type="public_channel")
    session.add(channel)
    session.commit()
    session.refresh(channel)
    return channel


def _user(session, slack_id):
    existing = session.exec(
        select(SlackUser).where(SlackUser.slack_id == slack_id)
    ).first()
    if existing:
        return existing
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
        name="weekly throwback",
    )
    session.add(poll)
    session.commit()
    session.refresh(poll)
    return poll


def _window_chain(session, poll, channel, user, *started_at):
    """Weekly windows chained oldest to newest; the last window is
    still open, the way the live chain looks mid-week."""
    instances = []
    for started in started_at:
        announcement = _message(session, channel, user, ANNOUNCEMENT, started)
        instance = MusicPollInstance(
            music_poll_id=poll.id,
            slack_message_id=announcement.id,
            created_at=started,
        )
        session.add(instance)
        session.commit()
        session.refresh(instance)
        instances.append(instance)
    for older, newer in zip(instances, instances[1:]):
        older.next_instance_id = newer.id
    session.commit()
    return instances


def _share(session, instance, channel, user, ts, url, *, recorded_at=None):
    """A song link shared inside one window's week.

    The row's recording time defaults to the share time; pass
    `recorded_at` when the pickup recorded it later.
    """
    message = _message(session, channel, user, url, ts)
    response = MusicPollResponse(
        music_poll_instance_id=instance.id,
        slack_user_id=user.id,
        slack_message_id=message.id,
        created_at=recorded_at if recorded_at is not None else ts,
        url=url,
    )
    session.add(response)
    session.commit()
    return response


def _posted_run(
    session,
    run_at,
    identities,
    *,
    slack_channel_slack_id=CHANNEL_SLACK_ID,
    manual=False,
):
    """One past run-posted poll, with the options it posted."""
    run = ScheduledPollRun(
        run_identity=run_at.isoformat(),
        slack_channel_slack_id=slack_channel_slack_id,
        run_at=run_at,
        scheduled_fire_time=None if manual else run_at,
        workflow_run_id="wfr-manual" if manual else None,
    )
    session.add(run)
    session.commit()
    session.refresh(run)
    for position, identity in enumerate(identities):
        session.add(
            ScheduledPollRunOption(
                scheduled_poll_run_id=run.id,
                position=position,
                song_identity=identity,
                song_link=identity,
                submitter_slack_user_slack_id="U_RUNNER",
                submission_date=run_at,
            )
        )
    session.commit()
    return run


def _pick(session, *, seed=0):
    return select_scheduled_poll_options(
        CHANNEL_SLACK_ID,
        NOW,
        session=session,
        rng=random.Random(seed),
    )


def _weeks_old(option: SelectedOption) -> float:
    return (NOW - option.submission_date) / datetime.timedelta(weeks=1)


def _spotify(name: str) -> str:
    return f"https://open.spotify.com/track/{name}"


# ----- song identity --------------------------------------------------


def test_spotify_identity_strips_query_and_takes_the_uri_form():
    assert song_identity(_spotify("7xGfFoTp4y6Q9jF9bZz0Y")) == (
        "spotify:7xGfFoTp4y6Q9jF9bZz0Y"
    )
    assert song_identity(
        "https://open.spotify.com/track/7xGfFoTp4y6Q9jF9bZz0Y?si=1a2b3c"
    ) == "spotify:7xGfFoTp4y6Q9jF9bZz0Y"
    assert song_identity("spotify:track:7xGfFoTp4y6Q9jF9bZz0Y") == (
        "spotify:7xGfFoTp4y6Q9jF9bZz0Y"
    )


def test_youtube_identity_covers_the_share_forms():
    assert song_identity("https://www.youtube.com/watch?v=dQw4w9WgXcQ") == (
        "youtube:dQw4w9WgXcQ"
    )
    assert song_identity(
        "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s"
    ) == "youtube:dQw4w9WgXcQ"
    assert song_identity("https://youtu.be/dQw4w9WgXcQ") == "youtube:dQw4w9WgXcQ"
    assert song_identity("https://www.youtube.com/shorts/dQw4w9WgXcQ") == (
        "youtube:dQw4w9WgXcQ"
    )
    assert song_identity("https://www.youtube.com/embed/dQw4w9WgXcQ") == (
        "youtube:dQw4w9WgXcQ"
    )


def test_other_urls_keep_their_tracking_free_form():
    assert song_identity(
        "https://example.com/song?utm_source=twitter&fbclid=abc&gclid=xyz"
    ) == "https://example.com/song"
    assert song_identity("https://example.com/song?keep=1&utm_campaign=weekly") == (
        "https://example.com/song?keep=1"
    )


def test_one_song_shared_through_different_url_forms_is_one_song(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 8, 31),
        datetime.datetime(2026, 9, 7),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # the same song shared as a URL and as a URI, a week
    # apart, by two different members
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 9, 1, 9),
        _spotify("7xGfFoTp4y6Q9jF9bZz0Y"),
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 9, 8, 9),
        "spotify:track:7xGfFoTp4y6Q9jF9bZz0Y",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 9, 2, 9),
        "https://songs.example/other-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 9, 3, 9),
        "https://songs.example/other-two",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 3
    # both shares normalized to one song, so the poll holds
    # it once, whatever form it was shared in
    assert [o.song_identity for o in options].count(
        "spotify:7xGfFoTp4y6Q9jF9bZz0Y"
    ) == 1


# ----- earliest-submission attribution --------------------------------


def test_a_shared_song_is_attributed_to_its_earliest_submission(session):
    channel = _channel(session)
    first, second = _user(session, "U1"), _user(session, "U2")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        first,
        datetime.datetime(2026, 8, 17),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 7),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    url = "https://songs.example/throwback"
    _share(session, instances[0], channel, first, datetime.datetime(2026, 8, 18, 9), url)
    _share(session, instances[2], channel, second, datetime.datetime(2026, 9, 8, 9), url)
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/other-one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/other-two",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 3
    shared = next(o for o in options if o.song_identity == url)
    assert shared.submitter_slack_user_slack_id == "U1"
    assert shared.submission_date == datetime.datetime(2026, 8, 18, 9, tzinfo=datetime.UTC)


def test_a_re_share_keeps_the_earliest_submission_and_stays_eligible(session):
    channel = _channel(session)
    poster, resharer = _user(session, "U1"), _user(session, "U2")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 8, 17),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    url = "https://songs.example/reshared"
    # the earliest submission sits in an eligible window
    _share(session, instances[0], channel, poster, datetime.datetime(2026, 8, 18, 9), url)
    # the re-share lands in the most recent closed window, which
    # the grace period covers -- it must not change the song's
    # window, date, submitter or eligibility
    _share(session, instances[3], channel, resharer, datetime.datetime(2026, 9, 22, 9), url)
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 8, 19, 9),
        "https://songs.example/one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/two",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U5"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/three",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 4
    reshared = next(o for o in options if o.song_identity == url)
    assert reshared.submitter_slack_user_slack_id == "U1"
    assert reshared.submission_date == datetime.datetime(2026, 8, 18, 9, tzinfo=datetime.UTC)


def test_a_song_is_dated_by_its_message_not_its_recording(session):
    channel = _channel(session)
    poster = _user(session, "U1")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 8, 31),
        datetime.datetime(2026, 9, 7),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    url = "https://songs.example/late-recording"
    # the song was shared in the first window's week, but
    # the pickup recorded the row only this week
    _share(
        session,
        instances[0],
        channel,
        poster,
        datetime.datetime(2026, 8, 31),
        url,
        recorded_at=datetime.datetime(2026, 9, 29, 9),
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 9, 2, 9),
        "https://songs.example/one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 9, 3, 9),
        "https://songs.example/two",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 9, 8, 9),
        "https://songs.example/three",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 4
    late = next(o for o in options if o.song_identity == url)
    # five weeks old by the message, not under a week old
    # by the recording -- the age and slot come from the
    # message's original post time, read as UTC
    assert late.submission_date == datetime.datetime(
        2026, 8, 31, tzinfo=datetime.UTC
    )
    assert _weeks_old(late) == pytest.approx(5.0)


# ----- exclusion: the grace period ------------------------------------


def test_the_two_most_recent_closed_windows_are_the_grace_period(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 8, 31),
        datetime.datetime(2026, 9, 7),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # the most recent closed window
    _share(
        session,
        instances[3],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 9, 22, 9),
        "https://songs.example/grace-one",
    )
    # the second most recent closed window
    _share(
        session,
        instances[2],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 9, 15, 9),
        "https://songs.example/grace-two",
    )
    # three windows back and further: eligible
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 9, 8, 9),
        "https://songs.example/old-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 9, 1, 9),
        "https://songs.example/old-two",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U5"),
        datetime.datetime(2026, 9, 2, 9),
        "https://songs.example/old-three",
    )

    options = _pick(session)

    assert options is not None
    assert sorted(o.song_identity for o in options) == [
        "https://songs.example/old-one",
        "https://songs.example/old-three",
        "https://songs.example/old-two",
    ]


def test_window_order_comes_from_the_instance_chain_not_timestamps(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    # four windows chained oldest to newest, but their
    # created_at stamps run newest to oldest: only the
    # next_instance_id chain says which windows are
    # the most recent closed ones
    started = [
        datetime.datetime(2026, 8, 31),
        datetime.datetime(2026, 9, 7),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
    ]
    stamps = [
        datetime.datetime(2026, 9, 28),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 7),
    ]
    instances = []
    for week, stamp in zip(started, stamps):
        announcement = _message(
            session, channel, poster, ANNOUNCEMENT, week
        )
        instance = MusicPollInstance(
            music_poll_id=poll.id,
            slack_message_id=announcement.id,
            created_at=stamp,
        )
        session.add(instance)
        session.commit()
        session.refresh(instance)
        instances.append(instance)
    for older, newer in zip(instances, instances[1:]):
        older.next_instance_id = newer.id
    session.commit()

    # songs in the two most recent closed windows by the
    # chain -- the first only looks old by its stamp
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 9, 8, 9),
        "https://songs.example/chain-grace-one",
    )
    _share(
        session,
        instances[2],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 9, 15, 9),
        "https://songs.example/chain-grace-two",
    )
    # songs in the chain's oldest closed window, whose
    # stamp makes it look like the most recent one
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 9, 1, 9),
        "https://songs.example/chain-pick-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 9, 2, 9),
        "https://songs.example/chain-pick-two",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U5"),
        datetime.datetime(2026, 9, 3, 9),
        "https://songs.example/chain-pick-three",
    )

    options = _pick(session)

    assert options is not None
    # the stamp-ordered window would have swallowed the
    # chain-pick songs and spared chain-grace-one
    assert sorted(o.song_identity for o in options) == [
        "https://songs.example/chain-pick-one",
        "https://songs.example/chain-pick-three",
        "https://songs.example/chain-pick-two",
    ]


def test_a_song_only_in_the_open_window_is_not_a_candidate(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 8, 31),
        datetime.datetime(2026, 9, 7),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # songs shared this week, in the still-open window
    for user_id, url in [
        ("U1", "https://songs.example/this-week-one"),
        ("U2", "https://songs.example/this-week-two"),
        ("U3", "https://songs.example/this-week-three"),
    ]:
        _share(
            session,
            instances[4],
            channel,
            _user(session, user_id),
            datetime.datetime(2026, 9, 29, 9),
            url,
        )

    # nothing is eligible, so the week is skipped
    assert _pick(session) is None


# ----- exclusion: the 8-poll no-repeat ------------------------------


def test_songs_featured_in_the_last_eight_polls_do_not_repeat(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 7),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # nine run-posted polls; the oldest sits outside the last
    # eight, the rest -- including a manual run -- inside them
    _posted_run(session, datetime.datetime(2026, 7, 6), ["https://songs.example/history-old"])
    _posted_run(
        session,
        datetime.datetime(2026, 7, 13),
        ["https://songs.example/recently-featured"],
        manual=True,
    )
    for week in range(8):
        _posted_run(
            session,
            datetime.datetime(2026, 7, 20) + datetime.timedelta(weeks=week),
            [f"https://songs.example/filler-{week}"],
        )
    # eligible songs, all shared in windows outside the grace period
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/history-old",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/recently-featured",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 9, 8, 9),
        "https://songs.example/fresh-one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 9, 9, 9),
        "https://songs.example/fresh-two",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U5"),
        datetime.datetime(2026, 8, 27, 9),
        "https://songs.example/fresh-three",
    )

    options = _pick(session)

    assert options is not None
    # the song featured nine polls ago is fair game again; the one
    # featured by the manual run eight polls ago is not
    assert sorted(o.song_identity for o in options) == [
        "https://songs.example/fresh-one",
        "https://songs.example/fresh-three",
        "https://songs.example/fresh-two",
        "https://songs.example/history-old",
    ]


def test_songs_featured_only_before_the_last_eight_polls_keep_their_appearance_count(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 13),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # five polls older than the last eight all featured the same
    # song, so it is eligible again but heavily featured
    for week in range(5):
        _posted_run(
            session,
            datetime.datetime(2026, 7, 6) + datetime.timedelta(weeks=week),
            ["https://songs.example/often-featured"],
        )
    for week in range(8):
        _posted_run(
            session,
            datetime.datetime(2026, 8, 10) + datetime.timedelta(weeks=week),
            [f"https://songs.example/filler-{week}"],
        )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/often-featured",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/fresh-one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 8, 27, 9),
        "https://songs.example/fresh-two",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 7, 14, 9),
        "https://songs.example/goldie-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U5"),
        datetime.datetime(2026, 7, 15, 9),
        "https://songs.example/goldie-two",
    )

    options = _pick(session)

    assert options is not None
    # the zero-appearance songs surface before the featured one
    assert sorted(o.song_identity for o in options) == [
        "https://songs.example/fresh-one",
        "https://songs.example/fresh-two",
        "https://songs.example/goldie-one",
        "https://songs.example/goldie-two",
    ]


# ----- submitter variety ----------------------------------------------


def test_options_come_from_distinct_submitters_whenever_possible(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 13),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # submitter A holds the two zero-appearance recent songs; the
    # slot split would take both, but four distinct submitters
    # exist, so A's second song yields to B's
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/a-one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/a-two",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_B"),
        datetime.datetime(2026, 8, 27, 9),
        "https://songs.example/b-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_C"),
        datetime.datetime(2026, 7, 14, 9),
        "https://songs.example/c-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_D"),
        datetime.datetime(2026, 7, 15, 9),
        "https://songs.example/d-one",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 4
    identities = [o.song_identity for o in options]
    assert "https://songs.example/b-one" in identities
    assert "https://songs.example/c-one" in identities
    assert "https://songs.example/d-one" in identities
    assert identities.count("https://songs.example/a-one") + identities.count(
        "https://songs.example/a-two"
    ) == 1
    assert len({o.submitter_slack_user_slack_id for o in options}) == 4


def test_a_submitter_appears_twice_once_distinct_submitters_run_out(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 13),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/a-recent",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 7, 14, 9),
        "https://songs.example/a-goldie",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_B"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/b-recent",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_C"),
        datetime.datetime(2026, 7, 15, 9),
        "https://songs.example/c-goldie",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 4
    submitters = [o.submitter_slack_user_slack_id for o in options]
    assert submitters.count("U_A") == 2
    assert submitters.count("U_B") == 1
    assert submitters.count("U_C") == 1


def test_a_submitter_never_appears_more_than_twice(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 13),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # submitter A holds three eligible songs; the cap holds them
    # to two options even though the goldie slot could take all
    # three of A's songs
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/a-recent",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 7, 14, 9),
        "https://songs.example/a-goldie-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 7, 15, 9),
        "https://songs.example/a-goldie-two",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_B"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/b-recent",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_C"),
        datetime.datetime(2026, 7, 16, 9),
        "https://songs.example/c-goldie",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 4
    submitters = [o.submitter_slack_user_slack_id for o in options]
    assert submitters.count("U_A") == 2
    assert "https://songs.example/c-goldie" in [o.song_identity for o in options]


# ----- option count and recent/goldie slots -------------------------


def test_four_pickable_songs_fill_four_options_with_two_slots_each(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 13),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/recent-one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/recent-two",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 7, 14, 9),
        "https://songs.example/goldie-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U4"),
        datetime.datetime(2026, 7, 15, 9),
        "https://songs.example/goldie-two",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 4
    recent = [o for o in options if 2 <= _weeks_old(o) <= 10]
    goldies = [o for o in options if _weeks_old(o) > 10]
    assert sorted(o.song_identity for o in recent) == [
        "https://songs.example/recent-one",
        "https://songs.example/recent-two",
    ]
    assert sorted(o.song_identity for o in goldies) == [
        "https://songs.example/goldie-one",
        "https://songs.example/goldie-two",
    ]


def test_three_pickable_songs_fill_three_options(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 13),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/recent-one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/recent-two",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 7, 14, 9),
        "https://songs.example/goldie-one",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 3
    recent = [o for o in options if 2 <= _weeks_old(o) <= 10]
    goldies = [o for o in options if _weeks_old(o) > 10]
    assert sorted(o.song_identity for o in recent) == [
        "https://songs.example/recent-one",
        "https://songs.example/recent-two",
    ]
    assert [o.song_identity for o in goldies] == ["https://songs.example/goldie-one"]


def test_the_recent_slot_spans_two_to_ten_weeks(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 26),
        datetime.datetime(2026, 7, 27),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    # exactly ten weeks old at run time: still recent
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 7, 27),
        "https://songs.example/ten-weeks",
    )
    # ten weeks and a day: a goldie
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 7, 26),
        "https://songs.example/goldie",
    )
    _share(
        session,
        instances[2],
        channel,
        _user(session, "U3"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/recent",
    )

    options = _pick(session)

    assert options is not None
    assert len(options) == 3
    recent = sorted(o.song_identity for o in options if 2 <= _weeks_old(o) <= 10)
    goldies = [o.song_identity for o in options if _weeks_old(o) > 10]
    assert recent == ["https://songs.example/recent", "https://songs.example/ten-weeks"]
    assert goldies == ["https://songs.example/goldie"]


def test_a_song_younger_than_the_recent_floor_waits_for_next_week(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    # irregular windows leave the third-most-recent closed window
    # only thirteen days old, so its songs are eligible but not
    # yet two weeks old
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 9, 22),
        datetime.datetime(2026, 9, 25),
        datetime.datetime(2026, 9, 28),
        datetime.datetime(2026, 10, 2),
    )
    for user_id in ("U1", "U2", "U3"):
        _share(
            session,
            instances[0],
            channel,
            _user(session, user_id),
            datetime.datetime(2026, 9, 22, 12),
            f"https://songs.example/{user_id.lower()}",
        )

    # neither recent nor goldie yet, so nothing is pickable
    assert _pick(session) is None


def test_a_song_exactly_two_weeks_old_is_recent(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 25),
        datetime.datetime(2026, 9, 28),
        datetime.datetime(2026, 10, 2),
    )
    for user_id in ("U1", "U2", "U3"):
        _share(
            session,
            instances[0],
            channel,
            _user(session, user_id),
            datetime.datetime(2026, 9, 21),
            f"https://songs.example/{user_id.lower()}",
        )

    options = _pick(session)

    assert options is not None
    assert len(options) == 3
    assert all(2 <= _weeks_old(o) <= 10 for o in options)


# ----- fewest appearances, random tiebreak --------------------------


def test_tied_appearances_break_uniformly_at_random(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 7, 13),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/a-recent",
    )
    # two goldies from the same submitter, both zero-appearance:
    # the single goldie slot is a coin flip between them
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 7, 14, 9),
        "https://songs.example/a-goldie-one",
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U_A"),
        datetime.datetime(2026, 7, 15, 9),
        "https://songs.example/a-goldie-two",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U_B"),
        datetime.datetime(2026, 8, 26, 9),
        "https://songs.example/b-recent",
    )

    outcomes = {
        tuple(sorted(o.song_identity for o in (_pick(session, seed=s) or [])))
        for s in range(40)
    }

    assert outcomes == {
        ("https://songs.example/a-goldie-one", "https://songs.example/a-recent", "https://songs.example/b-recent"),
        ("https://songs.example/a-goldie-two", "https://songs.example/a-recent", "https://songs.example/b-recent"),
    }


# ----- the skip -------------------------------------------------------


def test_fewer_than_three_pickable_songs_skips_the_week(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    instances = _window_chain(
        session,
        poll,
        channel,
        poster,
        datetime.datetime(2026, 8, 17),
        datetime.datetime(2026, 8, 24),
        datetime.datetime(2026, 9, 14),
        datetime.datetime(2026, 9, 21),
        datetime.datetime(2026, 9, 28),
    )
    _share(
        session,
        instances[0],
        channel,
        _user(session, "U1"),
        datetime.datetime(2026, 8, 18, 9),
        "https://songs.example/one",
    )
    _share(
        session,
        instances[1],
        channel,
        _user(session, "U2"),
        datetime.datetime(2026, 8, 25, 9),
        "https://songs.example/two",
    )

    assert _pick(session) is None


def test_a_channel_without_music_polls_has_no_options(session):
    _channel(session)

    assert _pick(session) is None


def test_a_channel_with_no_closed_window_has_no_options(session):
    channel = _channel(session)
    poster = _user(session, "U_ANNOUNCE")
    poll = _poll(session, channel)
    # the very first week: the only window is still open
    _window_chain(session, poll, channel, poster, datetime.datetime(2026, 9, 28))

    assert _pick(session) is None


# ----- the activity wiring --------------------------------------------


def test_the_selection_activity_is_registered_with_the_worker():
    from friendly_computing_machine.src.friendly_computing_machine.temporal.db.music_poll_activity import (
        select_music_poll_options_activity,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.worker import (
        ACTIVITIES,
    )

    assert select_music_poll_options_activity in ACTIVITIES
