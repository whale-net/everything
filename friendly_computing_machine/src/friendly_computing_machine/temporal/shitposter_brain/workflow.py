"""ShitposterBrainJobWorkflow: one brain job run for one persona.

Begin takes the per-persona lock or records a skip and returns. Otherwise the
body (compute, then one-transaction apply) races a per-kind timer. A timeout or
body failure marks the run failed and fails the workflow, so it shows failed
in the Temporal UI.
"""

import asyncio
from datetime import timedelta

from temporalio import workflow
from temporalio.common import RetryPolicy
from temporalio.exceptions import ActivityError, ApplicationError

with workflow.unsafe.imports_passed_through():
    from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
        ShitposterBrainJobKind,
        ShitposterBrainJobStatus,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.activity import (
        apply_brain_job_activity,
        begin_brain_job_activity,
        compute_brain_job_activity,
        fail_brain_job_activity,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
        BRAIN_JOB_TIMEOUTS,
        ApplyParams,
        BrainJobInput,
        BrainJobResult,
        FailParams,
    )

_SHORT_TIMEOUT = timedelta(seconds=30)
_COMPUTE_RETRY = RetryPolicy(maximum_attempts=3)
_ONCE = RetryPolicy(maximum_attempts=1)


@workflow.defn
class ShitposterBrainJobWorkflow:
    @workflow.run
    async def run(self, params: BrainJobInput) -> BrainJobResult:
        kind = ShitposterBrainJobKind(params.job_kind)
        begin = await workflow.execute_activity(
            begin_brain_job_activity,
            params,
            start_to_close_timeout=_SHORT_TIMEOUT,
            retry_policy=_ONCE,
        )
        if not begin.acquired:
            return BrainJobResult(
                run_id=begin.run_id,
                status=ShitposterBrainJobStatus.SKIPPED.value,
                skip_reason=begin.skip_reason,
            )

        timeout = BRAIN_JOB_TIMEOUTS[kind]
        body = asyncio.ensure_future(self._body(params, begin.run_id, timeout))
        timer = asyncio.ensure_future(workflow.sleep(timeout))
        await asyncio.wait({body, timer}, return_when=asyncio.FIRST_COMPLETED)

        if not body.done():
            body.cancel()
            return await self._fail(begin.run_id, kind, f"timed out after {timeout}")
        timer.cancel()
        try:
            return body.result()
        except ActivityError as e:
            return await self._fail(begin.run_id, kind, str(e.cause or e))

    async def _body(
        self, params: BrainJobInput, run_id: int, timeout: timedelta
    ) -> BrainJobResult:
        payload = await workflow.execute_activity(
            compute_brain_job_activity,
            params,
            start_to_close_timeout=timeout,
            retry_policy=_COMPUTE_RETRY,
        )
        return await workflow.execute_activity(
            apply_brain_job_activity,
            ApplyParams(run_id=run_id, job_kind=params.job_kind, payload=payload),
            start_to_close_timeout=timeout,
            retry_policy=_ONCE,
        )

    async def _fail(
        self, run_id: int, kind: ShitposterBrainJobKind, error: str
    ) -> BrainJobResult:
        final = await workflow.execute_activity(
            fail_brain_job_activity,
            FailParams(run_id=run_id, error=error),
            start_to_close_timeout=_SHORT_TIMEOUT,
            retry_policy=_ONCE,
        )
        if final.status != ShitposterBrainJobStatus.FAILED.value:
            # the apply committed before the failure landed; the run stands as it finished
            return final
        raise ApplicationError(
            f"brain job {kind.value} failed: {error}",
            type="BrainJobFailed",
            non_retryable=True,
        )
