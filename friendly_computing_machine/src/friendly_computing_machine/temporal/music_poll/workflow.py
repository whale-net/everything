"""The weekly music-poll schedule workflow (krill M5).

Fired by a Temporal schedule every Monday 00:00 UTC, the run
fans out over the channels configured for music polls: it
picks each channel's options with the selection activity and
publishes them through the bolt-free publish module. The
workflow only orchestrates -- every selection, clock read and
randomness lives in the activities it calls -- and it
delegates its decisions to the plain functions below, so they
are unit-testable without a Temporal runtime (the repo's
workflow-test pattern, per temporal/whagent).
"""

import datetime
import logging
from dataclasses import dataclass, field
from typing import Any, Awaitable, Callable

from temporalio import workflow
from temporalio.client import ScheduleSpec

from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_selection import (
    SelectedOption,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.base import (
    AbstractScheduleWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.db.music_poll_activity import (
    get_music_poll_channels_activity,
    get_scheduled_poll_run_activity,
    post_scheduled_poll_activity,
    record_scheduled_poll_run_activity,
    select_music_poll_options_activity,
)

logger = logging.getLogger(__name__)

# the schedule's calendar spec: every Monday 00:00 UTC
WEEKLY_POLL_CRON = "0 0 * * MON"
# a run that starts more than this after its scheduled fire
# time is stale (FR 5af51154)
STALE_AFTER = datetime.timedelta(hours=24)

# per-channel outcomes, carried in the workflow result
POSTED = "posted"
# a retry of a run that already posted in the channel
ALREADY_POSTED = "already-posted"
# fewer than 3 pickable songs: no poll, the previous one stays open
SKIPPED_FEW_PICKABLE = "skipped-few-pickable"
# the Slack post failed; the recorded run row waits for a retry
POST_FAILED = "post-failed"

# an activity and its args, executed by whatever runs the
# workflow -- workflow.execute_activity in the workflow, a fake
# in tests
ExecuteActivity = Callable[..., Awaitable[Any]]


@dataclass
class WeeklyMusicPollParams:
    """Input for a manually started run.

    The schedule fires the workflow with no input, so any
    input marks the execution as a manual run: its identity is
    the workflow run id, not a scheduled fire time.
    """


@dataclass
class RunIdentity:
    """Who this run is, resolved before anything is posted."""

    # the run's identity: the scheduled fire time for scheduled
    # runs, the workflow run id for manual runs (FR 3dadce0a)
    run_identity: str
    # when the run's workflow task executed
    run_at: datetime.datetime
    # scheduled runs: the fire time the run was scheduled for
    scheduled_fire_time: datetime.datetime | None
    # manual runs: the Temporal workflow run id
    workflow_run_id: str | None
    # a scheduled run that starts more than STALE_AFTER after
    # its fire time
    stale: bool = False


@dataclass
class ChannelResult:
    """What the run did in one channel, for the workflow result."""

    slack_channel_slack_id: str
    status: str
    # the options the run picked (or would have picked)
    options: list[SelectedOption] = field(default_factory=list)
    # the poll the run posted, when it posted one
    poll_id: int | None = None
    error: str | None = None


def is_stale_run(
    start_time: datetime.datetime,
    scheduled_fire_time: datetime.datetime,
) -> bool:
    """A run that starts more than 24h after its scheduled fire time is stale."""
    return start_time - scheduled_fire_time > STALE_AFTER


def resolve_run_identity(
    start_time: datetime.datetime,
    workflow_start_time: datetime.datetime,
    workflow_run_id: str,
    params: WeeklyMusicPollParams | None,
) -> RunIdentity:
    """Resolve a run's identity, and whether it is stale.

    A scheduled run -- the schedule fires the workflow with no
    input -- is identified by its scheduled fire time, which is
    the workflow execution's start time: the schedule starts
    the execution at the fire time, so a run whose first task
    waited for a returning worker still knows the fire time it
    was scheduled for. A manual run -- any input -- is
    identified by its workflow execution and is never stale.
    """
    if params is None:
        fire_time = workflow_start_time
        return RunIdentity(
            run_identity=fire_time.isoformat(),
            run_at=start_time,
            scheduled_fire_time=fire_time,
            workflow_run_id=None,
            stale=is_stale_run(start_time, fire_time),
        )
    return RunIdentity(
        run_identity=workflow_run_id,
        run_at=start_time,
        scheduled_fire_time=None,
        workflow_run_id=workflow_run_id,
    )


async def run_for_channel(
    execute: ExecuteActivity,
    slack_channel_slack_id: str,
    identity: RunIdentity,
) -> ChannelResult:
    """Run the weekly poll for one channel.

    The run's history row is written before the Slack post,
    keyed by (run identity, channel), so a retried or replayed
    run can tell its poll was already posted: it neither posts
    a second poll nor closes the poll it posted. A run whose
    recorded picks were never posted (the post failed) finishes
    that post with the recorded options rather than picking
    again.
    """
    existing = await execute(
        get_scheduled_poll_run_activity,
        identity.run_identity,
        slack_channel_slack_id,
    )
    if existing is not None and existing.slack_message_ts is not None:
        logger.info(
            "weekly music poll run %s already posted poll %s in %s",
            identity.run_identity,
            existing.poll_id,
            slack_channel_slack_id,
        )
        return ChannelResult(
            slack_channel_slack_id=slack_channel_slack_id,
            status=ALREADY_POSTED,
            options=existing.options,
            poll_id=existing.poll_id,
        )

    if existing is not None:
        # a previous attempt of this run recorded its picks but
        # the Slack post never succeeded; finish the post with
        # the options the run already committed to
        options = existing.options
        run_row_id = existing.id
    else:
        options = await execute(
            select_music_poll_options_activity, slack_channel_slack_id
        )
        if options is None:
            # fewer than 3 pickable songs: no poll this week,
            # and the previous poll stays open
            logger.info(
                "weekly music poll skipped in %s: fewer than 3 pickable songs",
                slack_channel_slack_id,
            )
            return ChannelResult(
                slack_channel_slack_id=slack_channel_slack_id,
                status=SKIPPED_FEW_PICKABLE,
            )
        run_row_id = await execute(
            record_scheduled_poll_run_activity,
            identity.run_identity,
            slack_channel_slack_id,
            identity.run_at,
            identity.scheduled_fire_time,
            identity.workflow_run_id,
            options,
        )

    outcome = await execute(
        post_scheduled_poll_activity,
        slack_channel_slack_id,
        run_row_id,
        options,
    )
    if outcome.error is not None:
        return ChannelResult(
            slack_channel_slack_id=slack_channel_slack_id,
            status=POST_FAILED,
            options=options,
            error=outcome.error,
        )
    return ChannelResult(
        slack_channel_slack_id=slack_channel_slack_id,
        status=POSTED,
        options=options,
        poll_id=outcome.poll_id,
    )


async def run_for_channels(
    execute: ExecuteActivity, identity: RunIdentity
) -> list[ChannelResult]:
    """Fan the run out over every configured music-poll channel."""
    if identity.stale:
        # a stale run posts nothing, closes nothing and records
        # nothing toward the 8-poll history
        logger.warning(
            "weekly music poll run fired at %s started at %s, more than "
            "%s after its fire time; posting nothing, closing nothing, "
            "recording nothing",
            identity.scheduled_fire_time,
            identity.run_at,
            STALE_AFTER,
        )
        return []

    channels = await execute(get_music_poll_channels_activity)
    return [
        await run_for_channel(execute, channel, identity)
        for channel in channels
    ]


@workflow.defn
class WeeklyMusicPollWorkflow(AbstractScheduleWorkflow):
    """The weekly music throwback poll, scheduled Mondays 00:00 UTC."""

    def get_schedule_spec(self) -> ScheduleSpec:
        # every Monday 00:00 UTC. The schedule keeps Temporal's
        # default policy: a worker outage delays a fire until a
        # worker returns rather than skipping it, and
        # service-side missed fires are covered by the default
        # catchup window -- so no custom catch-up policy is set.
        return ScheduleSpec(
            cron_expressions=[WEEKLY_POLL_CRON],
            time_zone_name="UTC",
        )

    @workflow.run
    async def run(
        self, params: WeeklyMusicPollParams | None = None
    ) -> list[ChannelResult]:
        identity = resolve_run_identity(
            workflow.info().start_time,
            workflow.info().workflow_start_time,
            workflow.info().run_id,
            params,
        )
        return await run_for_channels(_execute_activity, identity)


async def _execute_activity(activity: Any, *args: Any) -> Any:
    return await workflow.execute_activity(
        activity,
        *args,
        schedule_to_close_timeout=datetime.timedelta(minutes=5),
        start_to_close_timeout=datetime.timedelta(minutes=4),
    )
