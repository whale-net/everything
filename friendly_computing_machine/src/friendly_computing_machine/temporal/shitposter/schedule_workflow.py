"""ShitposterChannelScheduleWorkflow: durable random-gap cadence for one channel.

Opt-out parks the run rather than ending it, so an opt-in that lands while the
stop is still being processed reaches the same run via signal-with-start.
"""

from collections.abc import Callable
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


async def _wait(cond: Callable[[], bool], timeout: timedelta | None) -> bool:
    """True if cond became true, False on timeout."""
    try:
        await workflow.wait_condition(cond, timeout=timeout)
        return True
    except TimeoutError:
        return False


@workflow.defn
class ShitposterChannelScheduleWorkflow:
    def __init__(self) -> None:
        self._stopped = False
        # bumped on every re-enable, so a pending gap restarts from that moment
        self._epoch = 0
        # bumped on every kill-switch resume
        self._resume_seq = 0
        # last gate read said silenced; a resume then restarts the pending gap
        self._silenced = False

    @workflow.signal
    def stop(self) -> None:
        self._stopped = True

    @workflow.signal
    def start(self) -> None:
        # no-op while running, so reconcile never resets a pending gap
        if self._stopped:
            self._stopped = False
            self._epoch += 1

    @workflow.signal
    def resume(self) -> None:
        self._resume_seq += 1

    async def _gate(self, channel: str):
        return await workflow.execute_activity(
            shitposter_gate_activity,
            channel,
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )

    def _note_gate(self, reason: str | None, allowed: bool) -> bool:
        if reason == "not_opted_in":
            self._stopped = True
        self._silenced = reason == "silenced"
        return allowed

    async def _slot_open(self, channel: str) -> bool:
        gate = await self._gate(channel)
        return self._note_gate(gate.reason, gate.allowed)

    async def _await_due(self, params: ScheduleParams, channel: str) -> None:
        """Blocks until a slot is due and the gate is open."""
        while True:
            if self._stopped:
                await workflow.wait_condition(lambda: not self._stopped)
            epoch, seq = self._epoch, self._resume_seq
            gap = draw_gap(params)
            if await _wait(
                lambda: self._stopped
                or self._epoch != epoch
                or (self._silenced and self._resume_seq != seq),
                gap,
            ):
                continue
            # captured before the gate read so a resume during it is not lost
            epoch, seq = self._epoch, self._resume_seq
            if await self._slot_open(channel):
                return
            # silenced or opted out: no post, then a fresh gap after any wake
            await _wait(
                lambda: self._stopped or self._epoch != epoch or self._resume_seq != seq,
                SILENCE_POLL,
            )

    @workflow.run
    async def run(self, params: ScheduleParams) -> None:
        channel = params.channel_slack_id
        for _ in range(params.slots_per_run):
            await self._await_due(params, channel)
            result = await workflow.execute_child_workflow(
                ShitpostWorkflow.run,
                ShitpostParams(channel_slack_id=channel, trigger="scheduled"),
                id=f"{workflow.info().workflow_id}-slot-{params.slot}",
            )
            params.slot += 1
            if result.outcome == ShitpostOutcome.SKIPPED_GATE:
                self._note_gate(result.reason, allowed=False)
            if result.outcome != ShitpostOutcome.POSTED:
                workflow.logger.warning(
                    "scheduled shitpost slot skipped: channel=%s outcome=%s reason=%s",
                    channel,
                    result.outcome,
                    result.reason,
                )

        # a stopped run parks here: stop state does not survive continue_as_new
        if self._stopped:
            await workflow.wait_condition(lambda: not self._stopped)
        workflow.continue_as_new(params)
