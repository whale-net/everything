"""The weekly music-poll schedule workflow (krill M5).

The workflow delegates to the plain functions in
temporal/music_poll/workflow.py, so the schedule,
staleness and idempotency decisions are exercised directly
against a fake activity executor that records every call.
The real-API counterpart -- the workflow run on a
time-skipping WorkflowEnvironment against the real
activities -- lives in
test_music_poll_workflow_environment.py.
"""

import asyncio
import datetime
import inspect
import logging
from typing import Any
from unittest.mock import AsyncMock, patch

import pytest
import temporalio.workflow

from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_selection import (
    SelectedOption,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.db.music_poll_activity import (
    PostScheduledPollOutcome,
    ScheduledPollRunRow,
    get_music_poll_channels_activity,
    get_scheduled_poll_run_activity,
    post_scheduled_poll_activity,
    record_scheduled_poll_run_activity,
    select_music_poll_options_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.music_poll.workflow import (
    ALREADY_POSTED,
    DRY_RUN,
    POSTED,
    POST_FAILED,
    SKIPPED_FEW_PICKABLE,
    STALE_AFTER,
    RunIdentity,
    WeeklyMusicPollParams,
    WeeklyMusicPollWorkflow,
    _execute_activity,
    is_stale_run,
    resolve_run_identity,
    run_for_channel,
    run_for_channels,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.worker import (
    ACTIVITIES,
    WORKFLOWS,
)

# a Monday fire time, and a run that starts on the same Monday
FIRE_TIME = datetime.datetime(2026, 10, 5, tzinfo=datetime.UTC)
START_TIME = FIRE_TIME + datetime.timedelta(minutes=30)
RUN_ID = "0b17f60e-1a2b-4c3d-8e4f-5a6b7c8d9e0f"


class FakeActivities:
    """A fake activity executor: records every call, replies from a table.

    A reply is either a value or a callable taking the activity's
    args, so per-channel behavior is easy to fake.
    """

    def __init__(self, replies: dict[str, Any] | None = None):
        self.replies = replies or {}
        self.calls: list[tuple[str, tuple[Any, ...]]] = []

    async def __call__(self, activity: Any, *args: Any) -> Any:
        self.calls.append((activity.__name__, args))
        reply = self.replies.get(activity.__name__)
        if callable(reply):
            return reply(*args)
        return reply

    def names(self) -> list[str]:
        return [name for name, _ in self.calls]


def _option(link: str, submitter: str = "U1") -> SelectedOption:
    return SelectedOption(
        song_identity=link,
        song_link=link,
        submitter_slack_user_slack_id=submitter,
        submission_date=datetime.datetime(2026, 8, 17, tzinfo=datetime.UTC),
    )


def _scheduled_identity(**overrides: Any) -> RunIdentity:
    kwargs: dict[str, Any] = {
        "run_identity": FIRE_TIME.isoformat(),
        "run_at": START_TIME,
        "scheduled_fire_time": FIRE_TIME,
        "workflow_run_id": None,
    }
    kwargs.update(overrides)
    return RunIdentity(**kwargs)


def _manual_identity(**overrides: Any) -> RunIdentity:
    kwargs: dict[str, Any] = {
        "run_identity": RUN_ID,
        "run_at": START_TIME,
        "scheduled_fire_time": None,
        "workflow_run_id": RUN_ID,
    }
    kwargs.update(overrides)
    return RunIdentity(**kwargs)


def _run_row(**overrides: Any) -> ScheduledPollRunRow:
    kwargs: dict[str, Any] = {
        "id": 7,
        "run_identity": FIRE_TIME.isoformat(),
        "slack_channel_slack_id": "C_MUSIC",
        "run_at": START_TIME,
        "scheduled_fire_time": FIRE_TIME,
        "workflow_run_id": None,
        "poll_id": None,
        "slack_message_ts": None,
        "options": [_option("https://open.spotify.com/track/abc")],
    }
    kwargs.update(overrides)
    return ScheduledPollRunRow(**kwargs)


def _posted_outcome() -> PostScheduledPollOutcome:
    return PostScheduledPollOutcome(
        poll_id=42,
        slack_message_ts="123.456",
        closed_previous_poll_id=41,
        error=None,
    )


# ----- the schedule and the staleness guard ------------------------


def test_a_run_started_exactly_24h_after_its_fire_time_is_not_stale():
    assert not is_stale_run(FIRE_TIME + STALE_AFTER, FIRE_TIME)
    assert not is_stale_run(
        FIRE_TIME + STALE_AFTER - datetime.timedelta(seconds=1), FIRE_TIME
    )


def test_a_run_started_more_than_24h_after_its_fire_time_is_stale():
    assert is_stale_run(
        FIRE_TIME + STALE_AFTER + datetime.timedelta(seconds=1), FIRE_TIME
    )
    assert is_stale_run(FIRE_TIME + datetime.timedelta(days=7), FIRE_TIME)


def test_a_scheduled_run_is_identified_by_its_fire_time():
    identity = resolve_run_identity(START_TIME, FIRE_TIME, RUN_ID, None)

    assert identity.run_identity == FIRE_TIME.isoformat()
    assert identity.scheduled_fire_time == FIRE_TIME
    assert identity.workflow_run_id is None
    assert not identity.stale


def test_a_manual_run_is_identified_by_its_workflow_execution():
    identity = resolve_run_identity(
        # a manual run is never stale, however long after the
        # workflow was created it executes
        FIRE_TIME + datetime.timedelta(days=30),
        FIRE_TIME,
        RUN_ID,
        WeeklyMusicPollParams(),
    )

    assert identity.run_identity == RUN_ID
    assert identity.scheduled_fire_time is None
    assert identity.workflow_run_id == RUN_ID
    assert not identity.stale


def test_a_scheduled_run_that_starts_late_is_marked_stale():
    identity = resolve_run_identity(
        # a worker came back more than 24h after the fire
        # time the schedule started this run for
        FIRE_TIME + datetime.timedelta(hours=25),
        FIRE_TIME,
        RUN_ID,
        None,
    )

    assert identity.stale
    assert identity.scheduled_fire_time == FIRE_TIME
    assert identity.run_identity == FIRE_TIME.isoformat()


def test_a_stale_run_posts_closes_and_records_nothing(caplog):
    activities = FakeActivities()
    identity = _scheduled_identity(
        run_at=FIRE_TIME + datetime.timedelta(hours=25),
        stale=True,
    )

    with caplog.at_level(logging.WARNING):
        results = asyncio.run(run_for_channels(activities, identity))

    assert results == []
    # not even the channel list was read, so nothing was
    # posted, closed or recorded anywhere
    assert activities.calls == []
    assert any(
        record.levelno == logging.WARNING for record in caplog.records
    )


# ----- idempotent runs under retries (FR 3dadce0a) ------------------


def test_a_retry_skips_a_channel_the_run_already_posted_in():
    activities = FakeActivities(
        {
            get_music_poll_channels_activity.__name__: ["C_MUSIC"],
            "get_scheduled_poll_run_activity": _run_row(
                poll_id=42, slack_message_ts="123.456"
            ),
        }
    )

    result = asyncio.run(
        run_for_channel(activities, "C_MUSIC", _scheduled_identity())
    )

    assert result.status == ALREADY_POSTED
    assert result.poll_id == 42
    # the only call is the idempotency check: the retry
    # neither selects again, records again, posts again nor
    # closes the poll the run posted
    assert activities.names() == ["get_scheduled_poll_run_activity"]


def test_a_run_whose_post_failed_finishes_it_with_the_recorded_options():
    recorded = _run_row(
        options=[
            _option("https://open.spotify.com/track/abc"),
            _option("https://youtu.be/xyz", "U2"),
        ]
    )
    activities = FakeActivities(
        {
            "get_scheduled_poll_run_activity": recorded,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    result = asyncio.run(
        run_for_channel(activities, "C_MUSIC", _scheduled_identity())
    )

    assert result.status == POSTED
    # the run's picks were already recorded, so the retry does
    # not select or record again -- it posts the recorded
    # options under the recorded row
    assert activities.names() == [
        "get_scheduled_poll_run_activity",
        "post_scheduled_poll_activity",
    ]
    _post_name, post_args = activities.calls[-1]
    assert post_args[0] == "C_MUSIC"
    assert post_args[1] == recorded.id
    assert post_args[2] == recorded.options


def test_the_run_row_is_written_before_the_slack_post():
    activities = FakeActivities(
        {
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": [
                _option("https://open.spotify.com/track/abc")
            ],
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    result = asyncio.run(
        run_for_channel(activities, "C_MUSIC", _scheduled_identity())
    )

    assert result.status == POSTED
    names = activities.names()
    assert names.index("record_scheduled_poll_run_activity") < names.index(
        "post_scheduled_poll_activity"
    )


# ----- the weekly fan-out --------------------------------------------


def test_the_run_fans_out_over_every_configured_channel():
    activities = FakeActivities(
        {
            "get_music_poll_channels_activity": ["C_MUSIC", "C_JAMS"],
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": [
                _option("https://open.spotify.com/track/abc")
            ],
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    results = asyncio.run(
        run_for_channels(activities, _scheduled_identity())
    )

    assert [r.slack_channel_slack_id for r in results] == [
        "C_MUSIC",
        "C_JAMS",
    ]
    assert all(r.status == POSTED for r in results)
    # every configured channel was selected, recorded and posted
    assert activities.names().count("select_music_poll_options_activity") == 2
    assert activities.names().count("record_scheduled_poll_run_activity") == 2
    assert activities.names().count("post_scheduled_poll_activity") == 2


def test_a_channel_with_few_pickable_songs_is_skipped_and_stays_open():
    def pick(channel: str):
        return (
            None
            if channel == "C_JAMS"
            else [_option("https://open.spotify.com/track/abc")]
        )

    activities = FakeActivities(
        {
            "get_music_poll_channels_activity": ["C_MUSIC", "C_JAMS"],
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": pick,
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    results = asyncio.run(
        run_for_channels(activities, _scheduled_identity())
    )

    by_channel = {r.slack_channel_slack_id: r for r in results}
    assert by_channel["C_MUSIC"].status == POSTED
    # fewer than 3 pickable songs: no poll, and the previous
    # poll stays open -- nothing recorded or posted
    assert by_channel["C_JAMS"].status == SKIPPED_FEW_PICKABLE
    assert activities.names().count("record_scheduled_poll_run_activity") == 1
    assert activities.names().count("post_scheduled_poll_activity") == 1


def test_a_failed_post_is_reported_for_the_channel():
    activities = FakeActivities(
        {
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": [
                _option("https://open.spotify.com/track/abc")
            ],
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": PostScheduledPollOutcome(
                poll_id=None,
                slack_message_ts=None,
                closed_previous_poll_id=None,
                error=(
                    "I couldn't post the poll here. "
                    "Invite me to this channel and try again."
                ),
            ),
        }
    )

    result = asyncio.run(
        run_for_channel(activities, "C_MUSIC", _scheduled_identity())
    )

    assert result.status == POST_FAILED
    assert result.error is not None


# ----- manual runs from the Temporal UI (FR f7626736) ------------


def test_a_manual_run_with_no_params_fans_out_like_a_scheduled_run():
    activities = FakeActivities(
        {
            "get_music_poll_channels_activity": ["C_MUSIC", "C_JAMS"],
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": [
                _option("https://open.spotify.com/track/abc")
            ],
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    results = asyncio.run(
        run_for_channels(
            activities, _manual_identity(), WeeklyMusicPollParams()
        )
    )

    # no channel and no dry-run flag: every configured
    # channel, selected, recorded and posted like a
    # scheduled run
    assert [r.slack_channel_slack_id for r in results] == [
        "C_MUSIC",
        "C_JAMS",
    ]
    assert all(r.status == POSTED for r in results)
    assert activities.names().count("select_music_poll_options_activity") == 2
    assert activities.names().count("record_scheduled_poll_run_activity") == 2
    assert activities.names().count("post_scheduled_poll_activity") == 2


def test_a_channel_limited_run_touches_only_that_channel():
    activities = FakeActivities(
        {
            "get_music_poll_channels_activity": ["C_MUSIC", "C_JAMS"],
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": [
                _option("https://open.spotify.com/track/abc")
            ],
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    results = asyncio.run(
        run_for_channels(
            activities,
            _manual_identity(),
            WeeklyMusicPollParams(channel="C_JAMS"),
        )
    )

    # only the limited channel ran -- the channel list
    # was never fetched, and no activity touched
    # C_MUSIC
    assert [r.slack_channel_slack_id for r in results] == ["C_JAMS"]
    assert results[0].status == POSTED
    assert activities.names() == [
        "get_scheduled_poll_run_activity",
        "select_music_poll_options_activity",
        "record_scheduled_poll_run_activity",
        "post_scheduled_poll_activity",
    ]
    for _name, args in activities.calls:
        assert "C_MUSIC" not in args


def test_a_dry_run_returns_the_options_it_would_have_picked():
    options = [
        _option("https://open.spotify.com/track/abc"),
        _option("https://youtu.be/xyz", "U2"),
    ]
    activities = FakeActivities(
        {
            "get_music_poll_channels_activity": ["C_MUSIC", "C_JAMS"],
            "select_music_poll_options_activity": options,
        }
    )

    results = asyncio.run(
        run_for_channels(
            activities,
            _manual_identity(),
            WeeklyMusicPollParams(dry_run=True),
        )
    )

    # the would-be options for every channel are the
    # workflow result
    assert [r.slack_channel_slack_id for r in results] == [
        "C_MUSIC",
        "C_JAMS",
    ]
    assert all(r.status == DRY_RUN for r in results)
    assert all(r.options == options for r in results)
    # a dry run only reads: the channel list and each
    # channel's picks -- no run rows, no posts, no
    # closes, so nothing toward the 8-poll history
    assert activities.names() == [
        "get_music_poll_channels_activity",
        "select_music_poll_options_activity",
        "select_music_poll_options_activity",
    ]


def test_a_dry_run_limited_to_one_channel_reports_only_that_channel():
    activities = FakeActivities(
        {
            "get_music_poll_channels_activity": ["C_MUSIC", "C_JAMS"],
            "select_music_poll_options_activity": [
                _option("https://open.spotify.com/track/abc")
            ],
        }
    )

    results = asyncio.run(
        run_for_channels(
            activities,
            _manual_identity(),
            WeeklyMusicPollParams(channel="C_MUSIC", dry_run=True),
        )
    )

    assert [r.slack_channel_slack_id for r in results] == ["C_MUSIC"]
    assert results[0].status == DRY_RUN
    assert activities.names() == ["select_music_poll_options_activity"]


def test_a_dry_run_reports_a_channel_with_few_pickable_songs():
    activities = FakeActivities(
        {
            "get_music_poll_channels_activity": ["C_JAMS"],
            "select_music_poll_options_activity": None,
        }
    )

    results = asyncio.run(
        run_for_channels(
            activities,
            _manual_identity(),
            WeeklyMusicPollParams(dry_run=True),
        )
    )

    # the dry run would have skipped the week and left
    # the previous poll open
    assert results[0].status == SKIPPED_FEW_PICKABLE
    assert results[0].options == []


def test_a_manual_run_is_a_distinct_real_run_in_the_same_week():
    # a scheduled run and a manual run in the same week,
    # each under its own identity
    activities = FakeActivities(
        {
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": [
                _option("https://open.spotify.com/track/abc")
            ],
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    scheduled = asyncio.run(
        run_for_channel(activities, "C_MUSIC", _scheduled_identity())
    )
    manual = asyncio.run(
        run_for_channel(activities, "C_MUSIC", _manual_identity())
    )

    # neither run is treated as the other's replay: both
    # are real runs that close, post and record toward
    # the 8-poll history, under distinct identities
    assert scheduled.status == POSTED
    assert manual.status == POSTED
    recorded_identities = [
        args[0]
        for name, args in activities.calls
        if name == "record_scheduled_poll_run_activity"
    ]
    assert recorded_identities == [FIRE_TIME.isoformat(), RUN_ID]


def test_a_replayed_manual_execution_does_not_post_twice():
    activities = FakeActivities(
        {
            "get_scheduled_poll_run_activity": _run_row(
                run_identity=RUN_ID,
                workflow_run_id=RUN_ID,
                poll_id=42,
                slack_message_ts="123.456",
            ),
        }
    )

    result = asyncio.run(
        run_for_channel(activities, "C_MUSIC", _manual_identity())
    )

    # the same execution id, retried or replayed: the
    # run already posted poll 42, so it neither selects,
    # records, posts again nor closes its own poll
    assert result.status == ALREADY_POSTED
    assert result.poll_id == 42
    assert activities.names() == ["get_scheduled_poll_run_activity"]


# ----- the worker registration ---------------------------------


def test_the_weekly_poll_workflow_and_its_activities_run_on_the_fcm_worker():
    # the workflow is served by the FCM Temporal worker on
    # the existing fcm-<env>-main task queue, and the taskpool
    # music-poll tasks keep running alongside it
    assert WeeklyMusicPollWorkflow in WORKFLOWS
    for activity in (
        get_music_poll_channels_activity,
        get_scheduled_poll_run_activity,
        record_scheduled_poll_run_activity,
        post_scheduled_poll_activity,
        select_music_poll_options_activity,
    ):
        assert activity in ACTIVITIES


# ----- the temporalio arg-passing shape (the root defect) ----


def test_the_pinned_temporalio_takes_at_most_one_positional_arg():
    # temporalio 1.18.1's execute_activity takes the
    # activity plus at most one positional arg, so a
    # multi-arg activity call must be passed via args=
    sig = inspect.signature(temporalio.workflow.execute_activity)
    sig.bind(post_scheduled_poll_activity)
    sig.bind(post_scheduled_poll_activity, "C_MUSIC")
    with pytest.raises(TypeError):
        sig.bind(post_scheduled_poll_activity, "C_MUSIC", 7, [])
    sig.bind(
        post_scheduled_poll_activity, args=["C_MUSIC", 7, []]
    )


def test_execute_activity_passes_multi_args_via_args_keyword():
    execute = AsyncMock()
    options = [_option("https://open.spotify.com/track/abc")]

    with patch.object(temporalio.workflow, "execute_activity", execute):
        asyncio.run(
            _execute_activity(
                post_scheduled_poll_activity, "C_MUSIC", 7, options
            )
        )

    # the args reach execute_activity as the args= list,
    # not splatted positionally
    execute.assert_awaited_once_with(
        post_scheduled_poll_activity,
        args=["C_MUSIC", 7, options],
        schedule_to_close_timeout=datetime.timedelta(minutes=5),
        start_to_close_timeout=datetime.timedelta(minutes=4),
    )


class SignatureFaithfulExecutor:
    """Stands in for temporalio's execute_activity.

    Mirrors the pinned 1.18.1 signature exactly -- the
    activity, at most one positional arg, then
    keyword-only parameters -- so a positional splat of
    a 2+-arg activity call raises TypeError here just
    as it does in a real workflow task.
    """

    def __init__(self, replies: dict[str, Any] | None = None):
        self.replies = replies or {}
        self.calls: list[tuple[str, tuple[Any, ...]]] = []

    async def __call__(
        self,
        activity: Any,
        arg: Any = None,
        *,
        args: list[Any] | None = None,
        **kwargs: Any,
    ) -> Any:
        params = (
            list(args)
            if args
            else [arg] if arg is not None else []
        )
        self.calls.append((activity.__name__, tuple(params)))
        reply = self.replies.get(activity.__name__)
        if callable(reply):
            return reply(*params)
        return reply


def test_a_full_run_survives_the_pinned_temporalio_signature():
    # the production failure this guards against: a
    # non-dry run calls 2+-arg activities, which a
    # positional splat passes in a form temporalio
    # 1.18.1 rejects with TypeError at workflow-task
    # time, leaving the run RUNNING forever
    options = [_option("https://open.spotify.com/track/abc")]
    executor = SignatureFaithfulExecutor(
        {
            "get_scheduled_poll_run_activity": None,
            "select_music_poll_options_activity": options,
            "record_scheduled_poll_run_activity": 7,
            "post_scheduled_poll_activity": _posted_outcome(),
        }
    )

    with patch.object(temporalio.workflow, "execute_activity", executor):
        result = asyncio.run(
            run_for_channel(
                _execute_activity, "C_MUSIC", _scheduled_identity()
            )
        )

    assert result.status == POSTED
    assert result.poll_id == 42
    # every activity ran through the real _execute_activity,
    # with its args delivered intact, in call order
    assert executor.calls == [
        (
            "get_scheduled_poll_run_activity",
            (FIRE_TIME.isoformat(), "C_MUSIC"),
        ),
        ("select_music_poll_options_activity", ("C_MUSIC",)),
        (
            "record_scheduled_poll_run_activity",
            (
                FIRE_TIME.isoformat(),
                "C_MUSIC",
                START_TIME,
                FIRE_TIME,
                None,
                options,
            ),
        ),
        (
            "post_scheduled_poll_activity",
            ("C_MUSIC", 7, options),
        ),
    ]
