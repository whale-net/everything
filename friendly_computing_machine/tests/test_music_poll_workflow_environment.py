"""The weekly music-poll workflow against the real temporalio API.

The repo's first WorkflowEnvironment harness: the workflow runs on
the in-process, time-skipping test server (no live Temporal, no
network) with a real worker executing the real music-poll
activities. The mock-based workflow tests cannot see how
_execute_activity passes activity args to workflow.execute_activity
-- temporalio 1.18.1 accepts at most one positional arg there -- so
the positional-splat defect shipped with every unit test green
while every live non-dry run hung RUNNING. This harness fails the
way a live run does when any call site splats 2+ activity args
positionally again: the workflow task keeps failing and the run
never completes.
"""

import asyncio
import datetime
import uuid
from unittest.mock import Mock

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, delete, select
from temporalio.testing import WorkflowEnvironment
from temporalio.worker import Worker
from temporalio.worker.workflow_sandbox import (
    SandboxedWorkflowRunner,
    SandboxRestrictions,
)

import friendly_computing_machine.src.friendly_computing_machine.temporal.db.music_poll_activity as music_poll_activity
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    init_engine,
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
    PollVote,
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
from friendly_computing_machine.src.friendly_computing_machine.temporal.db.music_poll_activity import (
    WEEKLY_POLL_QUESTION,
    get_music_poll_channels_activity,
    get_scheduled_poll_run_activity,
    post_scheduled_poll_activity,
    record_scheduled_poll_run_activity,
    select_music_poll_options_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.music_poll.workflow import (
    POSTED,
    ChannelResult,
    WeeklyMusicPollParams,
    WeeklyMusicPollWorkflow,
)

CHANNEL_SLACK_ID = "C_MUSIC"
BOT_USER_ID = "U_BOT"
TASK_QUEUE = "fcm-music-poll-test-main"
# A healthy run completes in seconds on the time-skipping server; a
# workflow task that keeps failing -- the positional-splat
# regression -- never completes, so the wait falls over instead of
# hanging the test the way the defect hung live runs.
RUN_TIMEOUT = datetime.timedelta(seconds=30)

TABLES = [
    SlackChannel.__table__,
    SlackUser.__table__,
    SlackMessage.__table__,
    MusicPoll.__table__,
    MusicPollInstance.__table__,
    MusicPollResponse.__table__,
    Poll.__table__,
    PollOption.__table__,
    PollVote.__table__,
    ScheduledPollRun.__table__,
    ScheduledPollRunOption.__table__,
]


@pytest.fixture(scope="module")
def engine():
    engine = create_engine(
        "sqlite://",
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    # the activities run their DAL calls on the shared engine
    # singleton, so the workflow's activity threads write where
    # this test reads
    init_engine(engine)
    return engine


@pytest.fixture(autouse=True)
def _empty_tables(engine):
    # the engine singleton outlives a test, so every test starts
    # from an empty schema
    with Session(engine) as session:
        for model in (
            ScheduledPollRunOption,
            ScheduledPollRun,
            PollVote,
            PollOption,
            Poll,
            MusicPollResponse,
            MusicPollInstance,
            MusicPoll,
            SlackMessage,
            SlackUser,
            SlackChannel,
        ):
            session.exec(delete(model))
        session.commit()


@pytest.fixture
def slack_client(monkeypatch):
    """The Slack web client the posting activity uses, mocked."""
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


def _seed(engine) -> None:
    """One music-poll channel with three eligible songs.

    The poll's instance chain runs i1 -> i2 -> i3 -> i4, where i4
    is the still-open tail, so the two most recent closed windows
    (i3, i2) are the grace period and only i1's submissions are
    eligible: two recent songs and one goldie, one submitter each.
    """
    now = datetime.datetime.now(datetime.UTC)
    with Session(engine) as session:
        channel = SlackChannel(
            slack_id=CHANNEL_SLACK_ID,
            name="music",
            channel_type="public_channel",
        )
        session.add(channel)
        session.flush()

        poll = MusicPoll(
            slack_channel_id=channel.id,
            start_date=now.date(),
            name="weekly",
        )
        session.add(poll)
        session.flush()

        users = {
            slack_id: SlackUser(
                slack_id=slack_id,
                name=f"user-{slack_id}",
                slack_team_slack_id="T1",
            )
            for slack_id in ("U1", "U2", "U3")
        }
        for user in users.values():
            session.add(user)
            session.flush()

        instances = []
        for _ in range(4):
            message = SlackMessage(
                slack_id=uuid.uuid4().hex,
                slack_team_slack_id="T1",
                slack_channel_slack_id=CHANNEL_SLACK_ID,
                slack_user_slack_id=BOT_USER_ID,
                text="weekly music poll window",
                ts=_naive_utc(now - datetime.timedelta(weeks=13)),
                thread_ts=None,
                parent_user_slack_id=None,
            )
            session.add(message)
            session.flush()
            instance = MusicPollInstance(
                music_poll_id=poll.id,
                slack_message_id=message.id,
            )
            session.add(instance)
            session.flush()
            instances.append(instance)
        for older, newer in zip(instances, instances[1:]):
            older.next_instance_id = newer.id

        # the eligible window's submissions: their dates and
        # submitters come from the Slack messages they were recorded
        # from, not from when the pickup recorded them
        for slack_id, url, weeks_ago in (
            ("U1", "https://open.spotify.com/track/aaa", 4),
            ("U2", "https://open.spotify.com/track/bbb", 5),
            ("U3", "https://youtu.be/ccc", 12),
        ):
            message = SlackMessage(
                slack_id=uuid.uuid4().hex,
                slack_team_slack_id="T1",
                slack_channel_slack_id=CHANNEL_SLACK_ID,
                slack_user_slack_id=slack_id,
                text=url,
                ts=_naive_utc(
                    now - datetime.timedelta(weeks=weeks_ago)
                ),
                thread_ts=None,
                parent_user_slack_id=None,
            )
            session.add(message)
            session.flush()
            session.add(
                MusicPollResponse(
                    music_poll_instance_id=instances[0].id,
                    slack_user_id=users[slack_id].id,
                    slack_message_id=message.id,
                    url=url,
                )
            )
        session.commit()


def _naive_utc(value: datetime.datetime) -> datetime.datetime:
    # slackmessage.ts is stored naive and read back as UTC
    return value.replace(tzinfo=None)


def _run_workflow(params: WeeklyMusicPollParams | None) -> list[ChannelResult]:
    """Run the weekly music-poll workflow on the test environment.

    The workflow and its activities run against the real temporalio
    API on the in-process, time-skipping server, so the arg-passing
    shape of _execute_activity is exercised by a real workflow task.
    """

    async def _run() -> list[ChannelResult]:
        async with await WorkflowEnvironment.start_time_skipping() as env:
            # the same sandboxed runner the production worker
            # runs, so the workflow is prepared and executed
            # exactly as it is live
            async with Worker(
                env.client,
                task_queue=TASK_QUEUE,
                workflows=[WeeklyMusicPollWorkflow],
                activities=[
                    get_music_poll_channels_activity,
                    get_scheduled_poll_run_activity,
                    select_music_poll_options_activity,
                    record_scheduled_poll_run_activity,
                    post_scheduled_poll_activity,
                ],
                workflow_runner=SandboxedWorkflowRunner(
                    restrictions=(
                        SandboxRestrictions.default.with_passthrough_all_modules()
                    )
                ),
            ):
                return await asyncio.wait_for(
                    env.client.execute_workflow(
                        WeeklyMusicPollWorkflow.run,
                        params,
                        id=f"music-poll-test-{uuid.uuid4().hex}",
                        task_queue=TASK_QUEUE,
                        run_timeout=RUN_TIMEOUT,
                    ),
                    timeout=RUN_TIMEOUT.total_seconds(),
                )

    return asyncio.run(_run())


def _posted_options(engine, run: ScheduledPollRun) -> list[str]:
    with Session(engine) as session:
        return [
            option.text
            for option in session.exec(
                select(PollOption)
                .where(PollOption.poll_id == run.poll_id)
                .order_by(PollOption.position)
            )
        ]


def test_scheduled_run_posts_the_weekly_poll(engine, slack_client):
    # the schedule fires the workflow with no input: the run is
    # identified by its scheduled fire time and fans out over every
    # configured music-poll channel
    _seed(engine)

    result = _run_workflow(None)

    # the run posted one poll in the seeded channel, with the three
    # eligible songs as its options
    assert len(result) == 1
    channel_result = result[0]
    assert channel_result.slack_channel_slack_id == CHANNEL_SLACK_ID
    assert channel_result.status == POSTED
    assert channel_result.poll_id is not None
    assert {option.song_identity for option in channel_result.options} == {
        "spotify:aaa",
        "spotify:bbb",
        "youtube:ccc",
    }

    # every activity in the chain executed: the run history the
    # record activity wrote (6 args through _execute_activity), the
    # poll the post activity published (3 args), and the Slack post
    with Session(engine) as session:
        runs = session.exec(
            select(ScheduledPollRun).where(
                ScheduledPollRun.slack_channel_slack_id == CHANNEL_SLACK_ID
            )
        ).all()
        assert len(runs) == 1
        run = runs[0]
        # a scheduled run: identified by its fire time, not a
        # workflow run id
        assert run.run_identity
        assert run.scheduled_fire_time is not None
        assert run.workflow_run_id is None
        assert run.poll_id == channel_result.poll_id
        assert run.slack_message_ts == "123.456"

        option_rows = session.exec(
            select(ScheduledPollRunOption).where(
                ScheduledPollRunOption.scheduled_poll_run_id == run.id
            )
        ).all()
        assert {row.song_identity for row in option_rows} == {
            "spotify:aaa",
            "spotify:bbb",
            "youtube:ccc",
        }

        poll = session.get(Poll, run.poll_id)
        assert poll.question == WEEKLY_POLL_QUESTION
        # the run's poll is automated and open, waiting for votes
        assert poll.automated
        assert poll.closed_at is None
        assert poll.creator_slack_user_slack_id == BOT_USER_ID
        assert poll.slack_channel_slack_id == CHANNEL_SLACK_ID

    # each option shows its song link, credited to its submitter
    for url in (
        "https://open.spotify.com/track/aaa",
        "https://open.spotify.com/track/bbb",
        "https://youtu.be/ccc",
    ):
        assert any(
            url in text for text in _posted_options(engine, run)
        )
    assert all(
        "shared by user-" in text for text in _posted_options(engine, run)
    )

    slack_client.chat_postMessage.assert_called_once()


def test_manual_run_limited_to_one_channel(engine, slack_client):
    # an operator starts the workflow from the Temporal UI with an
    # input: the run is identified by its workflow execution and
    # limited to the one channel
    _seed(engine)

    result = _run_workflow(
        WeeklyMusicPollParams(channel=CHANNEL_SLACK_ID)
    )

    assert len(result) == 1
    channel_result = result[0]
    assert channel_result.status == POSTED
    assert channel_result.poll_id is not None

    # a manual run counts toward the poll history: its identity is
    # the workflow execution, with no scheduled fire time
    with Session(engine) as session:
        run = session.exec(
            select(ScheduledPollRun).where(
                ScheduledPollRun.slack_channel_slack_id == CHANNEL_SLACK_ID
            )
        ).first()
        assert run is not None
        assert run.workflow_run_id is not None
        assert run.run_identity == run.workflow_run_id
        assert run.scheduled_fire_time is None
        assert run.poll_id == channel_result.poll_id
        assert len(_posted_options(engine, run)) == 3

    slack_client.chat_postMessage.assert_called_once()
