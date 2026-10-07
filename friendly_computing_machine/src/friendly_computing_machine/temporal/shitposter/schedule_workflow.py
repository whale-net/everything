"""ShitposterChannelScheduleWorkflow: durable random-gap cadence for one channel."""

from datetime import timedelta

from temporalio import workflow

with workflow.unsafe.imports_passed_through():
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
        shitposter_gate_activity,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
        ACTIVITY_TIMEOUT,
        GENERATION_MARGIN,
        SILENCE_POLL,
        ScheduleParams,
        ShitpostOutcome,
        ShitpostParams,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.workflow import (
        ShitpostWorkflow,
    )


def draw_gap(params: ScheduleParams) -> timedelta:
    """Uniform gap; the top is trimmed so generation time cannot push past the max."""
    low = params.min_gap_seconds
    high = params.max_gap_seconds
    if high - GENERATION_MARGIN.total_seconds() > low:
        high -= GENERATION_MARGIN.total_seconds()
    return timedelta(seconds=workflow.random().uniform(low, high))


@workflow.defn
class ShitposterChannelScheduleWorkflow:
    def __init__(self) -> None:
        self._stopped = False
        self._resumed = False

    @workflow.signal
    def stop(self) -> None:
        self._stopped = True

    @workflow.signal
    def resume(self) -> None:
        self._resumed = True

    async def _gate(self, channel: str):
        return await workflow.execute_activity(
            shitposter_gate_activity,
            channel,
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )

    @workflow.run
    async def run(self, params: ScheduleParams) -> None:
        channel = params.channel_slack_id
        for _ in range(params.slots_per_run):
            gap = draw_gap(params)
            try:
                await workflow.wait_condition(lambda: self._stopped, timeout=gap)
            except TimeoutError:
                pass
            if self._stopped:
                return

            gate = await self._gate(channel)
            if gate.reason == "not_opted_in":
                return
            if not gate.allowed:
                # silenced: skip the slot, wait for resume, then draw a fresh gap
                if not await self._wait_for_resume(channel):
                    return
                continue

            result = await workflow.execute_child_workflow(
                ShitpostWorkflow.run,
                ShitpostParams(channel_slack_id=channel, trigger="scheduled"),
                id=f"{workflow.info().workflow_id}-slot-{params.slot}",
            )
            params.slot += 1
            if result.outcome != ShitpostOutcome.POSTED:
                workflow.logger.warning(
                    "scheduled shitpost slot skipped: channel=%s outcome=%s reason=%s",
                    channel,
                    result.outcome,
                    result.reason,
                )

        if self._stopped:
            return
        workflow.continue_as_new(params)

    async def _wait_for_resume(self, channel: str) -> bool:
        """True once the gate reopens; False if the workflow should end."""
        while True:
            self._resumed = False
            try:
                await workflow.wait_condition(
                    lambda: self._resumed or self._stopped, timeout=SILENCE_POLL
                )
            except TimeoutError:
                pass
            if self._stopped:
                return False
            gate = await self._gate(channel)
            if gate.reason == "not_opted_in":
                return False
            if gate.allowed:
                return True
