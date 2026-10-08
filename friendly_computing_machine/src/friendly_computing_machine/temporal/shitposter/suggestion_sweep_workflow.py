"""ShitposterSuggestionSweepWorkflow: periodic expiry of pending persona suggestions.

The sweep inside the /shitpost suggest handler only runs on submission, so a
quiet channel would otherwise keep expired suggestions pending indefinitely.
"""

from datetime import timedelta

from temporalio import workflow
from temporalio.client import ScheduleIntervalSpec, ScheduleSpec

from friendly_computing_machine.src.friendly_computing_machine.temporal.base import (
    AbstractScheduleWorkflow,
)

with workflow.unsafe.imports_passed_through():
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
        drain_suggestion_replies_activity,
        expire_pending_suggestions_activity,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
        ACTIVITY_TIMEOUT,
    )

SWEEP_INTERVAL = timedelta(minutes=15)
# Replies are held while the kill switch is engaged, so a short interval bounds post latency.
REPLY_DRAIN_INTERVAL = timedelta(minutes=1)


@workflow.defn
class ShitposterSuggestionSweepWorkflow(AbstractScheduleWorkflow):
    def get_schedule_spec(self) -> ScheduleSpec:
        return ScheduleSpec(intervals=[ScheduleIntervalSpec(every=SWEEP_INTERVAL)])

    @workflow.run
    async def run(self) -> int:
        return await workflow.execute_activity(
            expire_pending_suggestions_activity,
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )


@workflow.defn
class ShitposterSuggestionReplyDrainWorkflow(AbstractScheduleWorkflow):
    """Periodically posts queued suggestion outcome replies (held while the kill switch is engaged)."""

    def get_schedule_spec(self) -> ScheduleSpec:
        return ScheduleSpec(intervals=[ScheduleIntervalSpec(every=REPLY_DRAIN_INTERVAL)])

    @workflow.run
    async def run(self) -> int:
        return await workflow.execute_activity(
            drain_suggestion_replies_activity,
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )
