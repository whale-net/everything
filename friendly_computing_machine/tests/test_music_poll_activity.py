"""The scheduled-run posting activities (krill M5).

The run-history read/write activities and the posting
activity, over in-memory SQLite with a mocked Slack web
client: the channel list, the run row's idempotency key,
the auto-close of the channel's previous scheduled poll,
and the stamping of the run's rows with the created
poll's ids.
"""

import asyncio
import datetime
from unittest.mock import Mock

import pytest
from slack_sdk.errors import SlackApiError
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, delete, select

import friendly_computing_machine.src.friendly_computing_machine.temporal.db.music_poll_activity as music_poll_activity
from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_selection import (
    SelectedOption,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    init_engine,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import (
    Base,
)
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
)
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
    PollOption,
    PollVote,
)
from friendly_computing_machine.src.friendly_computing_machine.models.scheduled_poll import (
    ScheduledPollRun,
    ScheduledPollRunOption,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
)

TABLES = [
    SlackChannel.__table__,
    MusicPoll.__table__,
    Poll.__table__,
    PollOption.__table__,
    PollVote.__table__,
    ScheduledPollRun.__table__,
    ScheduledPollRunOption.__table__,
]

CHANNEL_SLACK_ID = "C_MUSIC"
BOT_USER_ID = "U_BOT"
# SQLite returns timestamptz columns as naive datetimes, so
# the fixtures and assertions below use naive UTC, as the
# DAL tests over the same schema do
FIRE_TIME = datetime.datetime(2026, 10, 5)
RUN_IDENTITY = FIRE_TIME.isoformat()
POST_ERROR = (
    "I couldn't post the poll here. Invite me to this channel and try again."
)


@pytest.fixture(scope="module")
def engine():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    # the activities run their DAL calls on the shared
    # engine singleton, so the path under test writes
    # where this fixture reads
    init_engine(engine)
    return engine


@pytest.fixture(autouse=True)
def _empty_tables(engine):
    # the engine singleton outlives a test, so every
    # test starts from an empty schema
    with Session(engine) as session:
        for model in (
            ScheduledPollRunOption,
            ScheduledPollRun,
            PollVote,
            PollOption,
            Poll,
            MusicPoll,
            SlackChannel,
        ):
            session.exec(delete(model))
        session.commit()


@pytest.fixture
def client(monkeypatch):
    client = Mock()
    client.auth_test.return_value = {"user_id": BOT_USER_ID}
    client.chat_postMessage.return_value = {
        "channel": CHANNEL_SLACK_ID,
        "ts": "123.456",
    }
    monkeypatch.setattr(
        music_poll_activity, "get_slack_web_client", lambda: client
    )
    return client


# ----- fixtures over the in-memory schema -------------------------


def _channel(engine, slack_id: str = CHANNEL_SLACK_ID) -> SlackChannel:
    with Session(engine) as session:
        channel = SlackChannel(
            slack_id=slack_id, name="music", channel_type="public_channel"
        )
        session.add(channel)
        session.commit()
        session.refresh(channel)
        return channel


def _music_poll(engine, channel: SlackChannel) -> MusicPoll:
    with Session(engine) as session:
        poll = MusicPoll(
            slack_channel_id=channel.id,
            start_date=datetime.date(2026, 9, 7),
            name="weekly",
        )
        session.add(poll)
        session.commit()
        session.refresh(poll)
        return poll


def _poll(
    engine,
    channel_slack_id: str = CHANNEL_SLACK_ID,
    *,
    automated: bool = True,
    slack_message_ts: str | None = "old.000",
) -> Poll:
    with Session(engine) as session:
        poll = Poll(
            question="Weekly music throwback",
            slack_channel_slack_id=channel_slack_id,
            creator_slack_user_slack_id=BOT_USER_ID,
            automated=automated,
            slack_message_ts=slack_message_ts,
        )
        session.add(poll)
        session.flush()
        for position, text in enumerate(["one", "two"]):
            session.add(PollOption(poll_id=poll.id, position=position, text=text))
        session.commit()
        session.refresh(poll)
        return poll


def _run_row(
    engine,
    *,
    run_identity: str = RUN_IDENTITY,
    channel_slack_id: str = CHANNEL_SLACK_ID,
    poll: Poll | None = None,
    slack_message_ts: str | None = None,
    run_at: datetime.datetime = FIRE_TIME,
) -> ScheduledPollRun:
    with Session(engine) as session:
        run = ScheduledPollRun(
            run_identity=run_identity,
            slack_channel_slack_id=channel_slack_id,
            run_at=run_at,
            scheduled_fire_time=FIRE_TIME,
            workflow_run_id=None,
            poll_id=poll.id if poll is not None else None,
            slack_message_ts=slack_message_ts,
        )
        session.add(run)
        session.commit()
        session.refresh(run)
        return run


def _option_rows(engine, run: ScheduledPollRun) -> list[ScheduledPollRunOption]:
    with Session(engine) as session:
        rows = []
        for position, link in enumerate(
            ["https://open.spotify.com/track/abc", "https://youtu.be/xyz"]
        ):
            row = ScheduledPollRunOption(
                scheduled_poll_run_id=run.id,
                position=position,
                song_identity=link,
                song_link=link,
                submitter_slack_user_slack_id="U1",
                submission_date=datetime.datetime(2026, 8, 17),
            )
            session.add(row)
            rows.append(row)
        session.commit()
        return rows


def _recorded_options() -> list[SelectedOption]:
    return [
        SelectedOption(
            song_identity="https://open.spotify.com/track/abc",
            song_link="https://open.spotify.com/track/abc",
            submitter_slack_user_slack_id="U1",
            submission_date=datetime.datetime(2026, 8, 17),
        ),
        SelectedOption(
            song_identity="https://youtu.be/xyz",
            song_link="https://youtu.be/xyz",
            submitter_slack_user_slack_id="U2",
            submission_date=datetime.datetime(2026, 8, 10),
        ),
    ]


# ----- the channel list --------------------------------------------


def test_the_channel_list_activity_lists_every_music_poll_channel(engine):
    first = _channel(engine)
    second = _channel(engine, slack_id="C_JAMS")
    _music_poll(engine, first)
    _music_poll(engine, second)

    channels = asyncio.run(
        music_poll_activity.get_music_poll_channels_activity()
    )

    assert channels == [first.slack_id, second.slack_id]


# ----- the run history ---------------------------------------------


def test_the_run_row_activity_returns_the_row_and_its_options(engine):
    run = _run_row(engine)
    _option_rows(engine, run)

    row = asyncio.run(
        music_poll_activity.get_scheduled_poll_run_activity(
            RUN_IDENTITY, CHANNEL_SLACK_ID
        )
    )

    assert row is not None
    assert row.id == run.id
    assert row.run_identity == RUN_IDENTITY
    assert row.slack_channel_slack_id == CHANNEL_SLACK_ID
    assert row.scheduled_fire_time == FIRE_TIME
    assert [option.song_link for option in row.options] == [
        "https://open.spotify.com/track/abc",
        "https://youtu.be/xyz",
    ]


def test_the_run_row_activity_returns_nothing_for_an_unknown_run(engine):
    assert (
        asyncio.run(
            music_poll_activity.get_scheduled_poll_run_activity(
                "2026-09-28T00:00:00+00:00", CHANNEL_SLACK_ID
            )
        )
        is None
    )


def test_recording_a_run_writes_its_row_and_options(engine):
    run_id = asyncio.run(
        music_poll_activity.record_scheduled_poll_run_activity(
            RUN_IDENTITY,
            CHANNEL_SLACK_ID,
            FIRE_TIME + datetime.timedelta(minutes=5),
            FIRE_TIME,
            None,
            _recorded_options(),
        )
    )

    with Session(engine) as session:
        run = session.exec(
            select(ScheduledPollRun).where(
                ScheduledPollRun.run_identity == RUN_IDENTITY
            )
        ).one()
        assert run.id == run_id
        assert run.slack_channel_slack_id == CHANNEL_SLACK_ID
        assert run.scheduled_fire_time == FIRE_TIME
        assert run.workflow_run_id is None
        assert run.poll_id is None
        assert run.slack_message_ts is None
        option_rows = session.exec(
            select(ScheduledPollRunOption)
            .where(ScheduledPollRunOption.scheduled_poll_run_id == run.id)
            .order_by(ScheduledPollRunOption.position)
        ).all()
        assert [row.position for row in option_rows] == [0, 1]
        assert [row.song_identity for row in option_rows] == [
            "https://open.spotify.com/track/abc",
            "https://youtu.be/xyz",
        ]
        assert [row.submitter_slack_user_slack_id for row in option_rows] == [
            "U1",
            "U2",
        ]


def test_recording_a_run_twice_keeps_one_row(engine):
    first = asyncio.run(
        music_poll_activity.record_scheduled_poll_run_activity(
            RUN_IDENTITY,
            CHANNEL_SLACK_ID,
            FIRE_TIME,
            FIRE_TIME,
            None,
            _recorded_options(),
        )
    )
    second = asyncio.run(
        music_poll_activity.record_scheduled_poll_run_activity(
            RUN_IDENTITY,
            CHANNEL_SLACK_ID,
            FIRE_TIME,
            FIRE_TIME,
            None,
            _recorded_options(),
        )
    )

    # the unique key on (run identity, channel) makes the
    # second writer read back the row the first committed
    assert first == second
    with Session(engine) as session:
        runs = session.exec(
            select(ScheduledPollRun).where(
                ScheduledPollRun.run_identity == RUN_IDENTITY
            )
        ).all()
        assert len(runs) == 1


# ----- posting ------------------------------------------------------


def test_posting_closes_the_previous_scheduled_poll_and_posts(
    engine, client
):
    previous_poll = _poll(engine)
    previous_run = _run_row(
        engine,
        run_identity="2026-09-28T00:00:00+00:00",
        poll=previous_poll,
        run_at=FIRE_TIME - datetime.timedelta(days=7),
    )
    _option_rows(engine, previous_run)
    run = _run_row(engine)
    _option_rows(engine, run)

    outcome = asyncio.run(
        music_poll_activity.post_scheduled_poll_activity(
            CHANNEL_SLACK_ID,
            run.id,
            _recorded_options(),
        )
    )

    with Session(engine) as session:
        # the previous scheduled poll stopped accepting
        # votes and shows its final result
        closed = session.get(Poll, previous_poll.id)
        assert closed.closed_at is not None
        refresh = client.chat_update.call_args.kwargs
        assert refresh["channel"] == CHANNEL_SLACK_ID
        assert refresh["ts"] == "old.000"
        assert "*closed*" in str(refresh["blocks"])

        # the new poll was posted as an automated,
        # single-vote poll created by the bot
        posted = session.exec(
            select(Poll).where(Poll.id != previous_poll.id)
        ).one()
        assert posted.automated is True
        assert posted.creator_slack_user_slack_id == BOT_USER_ID
        assert posted.anonymous is False
        assert posted.vote_limit == 1
        assert posted.slack_message_ts == "123.456"
        assert client.chat_postMessage.call_args.kwargs["channel"] == (
            CHANNEL_SLACK_ID
        )

        # the run's rows are stamped with the created
        # poll's ids, so its picks stay queryable
        stamped_run = session.get(ScheduledPollRun, run.id)
        assert stamped_run.poll_id == posted.id
        assert stamped_run.slack_message_ts == "123.456"
        stamped_options = session.exec(
            select(ScheduledPollRunOption)
            .where(
                ScheduledPollRunOption.scheduled_poll_run_id == run.id
            )
            .order_by(ScheduledPollRunOption.position)
        ).all()
        posted_option_ids = session.exec(
            select(PollOption)
            .where(PollOption.poll_id == posted.id)
            .order_by(PollOption.position)
        ).all()
        assert [row.poll_option_id for row in stamped_options] == [
            option.id for option in posted_option_ids
        ]

    assert outcome.poll_id == posted.id
    assert outcome.slack_message_ts == "123.456"
    assert outcome.closed_previous_poll_id == previous_poll.id
    assert outcome.error is None


def test_posting_a_run_that_already_posted_neither_reposts_nor_reclose(
    engine, client
):
    previous_poll = _poll(engine)
    previous_run = _run_row(
        engine,
        run_identity="2026-09-28T00:00:00+00:00",
        poll=previous_poll,
        run_at=FIRE_TIME - datetime.timedelta(days=7),
    )
    _option_rows(engine, previous_run)
    # this run already posted a poll of its own earlier
    # in the same attempt
    own_poll = _poll(engine, slack_message_ts="already.000")
    run = _run_row(engine, poll=own_poll, slack_message_ts="already.000")
    _option_rows(engine, run)

    outcome = asyncio.run(
        music_poll_activity.post_scheduled_poll_activity(
            CHANNEL_SLACK_ID,
            run.id,
            _recorded_options(),
        )
    )

    assert outcome.poll_id == own_poll.id
    assert outcome.slack_message_ts == "already.000"
    # no second Slack post, and neither the poll the run
    # posted nor the channel's previous scheduled poll is
    # touched again
    client.chat_postMessage.assert_not_called()
    client.chat_update.assert_not_called()
    with Session(engine) as session:
        assert session.get(Poll, previous_poll.id).closed_at is None
        assert session.get(Poll, own_poll.id).closed_at is None


def test_posting_never_closes_a_poll_a_person_created(engine, client):
    user_poll = _poll(engine, automated=False)
    # even a run row that (pathologically) points at the
    # user's poll must not close it
    run = _run_row(engine, poll=user_poll)
    _option_rows(engine, run)

    outcome = asyncio.run(
        music_poll_activity.post_scheduled_poll_activity(
            CHANNEL_SLACK_ID,
            run.id,
            _recorded_options(),
        )
    )

    with Session(engine) as session:
        assert session.get(Poll, user_poll.id).closed_at is None
    # no close means no message refresh, only the new post
    client.chat_update.assert_not_called()
    client.chat_postMessage.assert_called_once()
    assert outcome.closed_previous_poll_id is None


def test_posting_with_no_previous_scheduled_poll_leaves_nothing_to_close(
    engine, client
):
    run = _run_row(engine)
    _option_rows(engine, run)

    outcome = asyncio.run(
        music_poll_activity.post_scheduled_poll_activity(
            CHANNEL_SLACK_ID,
            run.id,
            _recorded_options(),
        )
    )

    assert outcome.closed_previous_poll_id is None
    client.chat_update.assert_not_called()
    client.chat_postMessage.assert_called_once()


def test_a_failed_post_reports_the_error_and_leaves_the_run_unstamped(
    engine, client
):
    client.chat_postMessage.side_effect = SlackApiError(
        "not_in_channel", {"error": "not_in_channel"}
    )
    run = _run_row(engine)
    _option_rows(engine, run)

    outcome = asyncio.run(
        music_poll_activity.post_scheduled_poll_activity(
            CHANNEL_SLACK_ID,
            run.id,
            _recorded_options(),
        )
    )

    assert outcome.error == POST_ERROR
    assert outcome.poll_id is None
    with Session(engine) as session:
        stamped = session.get(ScheduledPollRun, run.id)
        assert stamped.poll_id is None
        assert stamped.slack_message_ts is None
