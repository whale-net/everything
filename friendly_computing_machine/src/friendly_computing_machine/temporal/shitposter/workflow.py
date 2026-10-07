"""ShitpostWorkflow: gate, generate, guardrails, re-gate, post and record.

The shared engine behind scheduled, summon and riff shitposts; callers pass
the trigger and principal and read the returned ShitpostResult.
"""

from temporalio import workflow

from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
    ShitpostOutcome,
    ShitpostParams,
    ShitpostResult,
)


@workflow.defn
class ShitpostWorkflow:
    @workflow.run
    async def run(self, params: ShitpostParams) -> ShitpostResult:
        raise NotImplementedError
