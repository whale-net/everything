"""The AbstractScheduleWorkflow schedule update path (krill M5).

get_schedule_update rebuilds the schedule for the
app_env it was created with. The schedule id is
wf-schedule-fcm-{app_env}-{ClassName}, so the app_env
is recovered from it -- previously the schedule id
itself was passed as the app_env, which rebuilt the
schedule under a workflow id built from the schedule
id.
"""

from types import SimpleNamespace
from datetime import timedelta

import pytest
from temporalio import workflow
from temporalio.client import (
    ScheduleIntervalSpec,
    SchedulePolicy,
    ScheduleSpec,
    ScheduleUpdateInput,
)

from friendly_computing_machine.src.friendly_computing_machine.temporal.base import (
    AbstractScheduleWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.music_poll.workflow import (
    WeeklyMusicPollWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    init_temporal,
)


@pytest.fixture(scope="module", autouse=True)
def temporal_config():
    # get_schedule builds its action on the main task
    # queue, whose name needs the temporal config
    init_temporal(host="temporal.example.com", app_env="test")


@workflow.defn
class _EveryTwoMinutesWorkflow(AbstractScheduleWorkflow):
    """A stand-in for the repo's real schedule workflows."""

    def get_schedule_spec(self) -> ScheduleSpec:
        return ScheduleSpec(
            intervals=[ScheduleIntervalSpec(every=timedelta(minutes=2))]
        )

    @workflow.run
    async def run(self, wf_arg=None):
        return None


def test_schedule_update_rebuilds_the_schedule_for_the_created_app_env():
    schedule_workflow = _EveryTwoMinutesWorkflow()
    # the id async_upsert_schedule creates the schedule under
    schedule_id = schedule_workflow.get_schedule_id("prod")
    assert schedule_id == "wf-schedule-fcm-prod-_EveryTwoMinutesWorkflow"

    update = schedule_workflow.get_schedule_update(
        ScheduleUpdateInput(description=SimpleNamespace(id=schedule_id))
    )

    # the update starts the workflow under the same workflow
    # id the schedule was created with, on the main task
    # queue -- not an id built from the schedule id itself
    action = update.schedule.action
    assert action.id == schedule_workflow.get_id("prod")
    assert action.id == "fcm-prod-_EveryTwoMinutesWorkflow"
    assert action.task_queue == schedule_workflow.get_temporal_queue_name()
    assert update.schedule.spec == schedule_workflow.get_schedule_spec()


def test_schedule_update_keeps_a_dashed_app_env_intact():
    schedule_workflow = _EveryTwoMinutesWorkflow()
    # an app_env may itself contain dashes
    schedule_id = schedule_workflow.get_schedule_id("prod-us-1")

    update = schedule_workflow.get_schedule_update(
        ScheduleUpdateInput(description=SimpleNamespace(id=schedule_id))
    )

    assert update.schedule.action.id == (
        "fcm-prod-us-1-_EveryTwoMinutesWorkflow"
    )


# ----- the weekly music-poll schedule (krill M5) -------------


def test_the_weekly_music_poll_schedule_fires_every_monday_0000_utc():
    spec = WeeklyMusicPollWorkflow().get_schedule_spec()

    # every Monday 00:00 UTC, and nothing else
    assert spec.cron_expressions == ["0 0 * * MON"]
    assert spec.time_zone_name == "UTC"
    assert spec.calendars == []
    assert spec.intervals == []
    assert spec.skip == []


def test_the_weekly_music_poll_schedule_keeps_the_default_policy():
    schedule = WeeklyMusicPollWorkflow().get_schedule("test")

    # the schedule keeps the pattern's default policy: a
    # worker outage delays a fire until a worker returns,
    # and service-side missed fires are covered by the
    # default catchup window -- so no custom policy is set
    assert schedule.policy == SchedulePolicy()
