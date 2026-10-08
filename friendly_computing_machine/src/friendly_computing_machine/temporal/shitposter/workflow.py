"""ShitpostWorkflow: gate, generate, guardrails, re-gate, post and record.

The shared engine behind scheduled, summon and riff shitposts; callers pass
the trigger and principal and read the returned ShitpostResult.
"""

from datetime import timedelta

from temporalio import workflow
from temporalio.common import RetryPolicy
from temporalio.exceptions import ActivityError

with workflow.unsafe.imports_passed_through():
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
        DraftPostParams,
        GenerateParams,
        NoticeParams,
        PostParams,
        SkipParams,
        check_guardrails_activity,
        generate_shitpost_activity,
        post_and_record_shitpost_activity,
        post_queued_draft_activity,
        record_scheduled_skip_activity,
        resolve_persona_activity,
        resolve_snapshot_activity,
        send_ephemeral_notice_activity,
        shitposter_gate_activity,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
        ACTIVITY_TIMEOUT,
        INTERACTIVE_DEADLINE,
        MAX_SCHEDULED_ATTEMPTS,
        NO_SHITPOST_NOTICE,
        SCHEDULED_DEADLINE,
        ShitpostOutcome,
        ShitpostParams,
        ShitpostResult,
    )

_NOTICE_TIMEOUT = timedelta(seconds=5)
# writer outcomes that make a scheduled slot a skip rather than a silent miss
_WRITER_UNAVAILABLE = (ShitpostOutcome.FAILED_GENERATION, ShitpostOutcome.TIMED_OUT)
_ONCE = RetryPolicy(maximum_attempts=1)


@workflow.defn
class ShitpostWorkflow:
    @workflow.run
    async def run(self, params: ShitpostParams) -> ShitpostResult:
        interactive = params.trigger != "scheduled"
        started = workflow.now()
        # interactive notice must still land inside the overall deadline
        budget = (INTERACTIVE_DEADLINE - _NOTICE_TIMEOUT) if interactive else SCHEDULED_DEADLINE

        gate = await workflow.execute_activity(
            shitposter_gate_activity,
            params.channel_slack_id,
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )
        if not gate.allowed:
            # gated out: nothing posts and no notice is owed
            return ShitpostResult(ShitpostOutcome.SKIPPED_GATE, reason=gate.reason)

        persona = await workflow.execute_activity(
            resolve_persona_activity,
            None,
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )
        if params.trigger == "scheduled" and not params.whagent_session_id:
            queued = await self._queued_draft(params, persona)
            if queued is not None:
                return queued
        # a riff on an existing session keeps the snapshot its session started from
        if params.whagent_session_id:
            snapshot_id, context_text = params.context_snapshot_id, None
        else:
            snapshot = await workflow.execute_activity(
                resolve_snapshot_activity,
                params.context_snapshot_id,
                start_to_close_timeout=ACTIVITY_TIMEOUT,
            )
            snapshot_id, context_text = snapshot.snapshot_id, snapshot.rendered_text

        attempts = 1 if interactive else MAX_SCHEDULED_ATTEMPTS
        result = ShitpostResult(ShitpostOutcome.BLOCKED_GUARDRAIL, reason="no_attempt")
        for attempt in range(attempts):
            remaining = budget - (workflow.now() - started)
            if remaining <= timedelta(seconds=1):
                result = ShitpostResult(ShitpostOutcome.TIMED_OUT, reason="deadline")
                break
            result, text, session_id = await self._attempt(
                params, persona, attempt, remaining, context_text, snapshot_id
            )
            if result is not None and result.outcome != ShitpostOutcome.BLOCKED_GUARDRAIL:
                break
            if attempt + 1 < attempts:
                workflow.logger.warning(
                    "shitpost blocked by guardrail (%s), regenerating (%d/%d)",
                    result.reason if result else None,
                    attempt + 1,
                    attempts,
                )
        if params.trigger == "scheduled" and result.outcome in _WRITER_UNAVAILABLE:
            await self._record_skip(params, persona, snapshot_id)
            result = ShitpostResult(
                ShitpostOutcome.SKIPPED_WRITER_UNAVAILABLE, reason="writer_unavailable"
            )
        if result.outcome != ShitpostOutcome.POSTED:
            if result.outcome == ShitpostOutcome.FAILED_GENERATION:
                workflow.logger.error("shitpost generation failed: %s", result.reason)
            if interactive and result.outcome != ShitpostOutcome.SKIPPED_GATE:
                await self._notice(params)
        return result

    async def _queued_draft(self, params: ShitpostParams, persona):
        """Post the best queued draft; None means generate on the spot."""
        try:
            return await workflow.execute_activity(
                post_queued_draft_activity,
                DraftPostParams(params=params, persona=persona),
                start_to_close_timeout=ACTIVITY_TIMEOUT,
                retry_policy=_ONCE,
            )
        except ActivityError:
            workflow.logger.error("queued draft post failed; generating on the spot")
            return None

    async def _attempt(
        self,
        params: ShitpostParams,
        persona,
        attempt: int,
        remaining: timedelta,
        context_text,
        snapshot_id,
    ):
        try:
            gen = await workflow.execute_activity(
                generate_shitpost_activity,
                GenerateParams(
                    params=params,
                    persona_text=persona.persona_text,
                    deadline_seconds=remaining.total_seconds(),
                    attempt=attempt,
                    context_text=context_text,
                ),
                start_to_close_timeout=remaining + timedelta(seconds=2),
                retry_policy=_ONCE,
            )
        except ActivityError as e:
            return ShitpostResult(ShitpostOutcome.TIMED_OUT, reason=str(e)), "", None
        if not gen.ok:
            outcome = (
                ShitpostOutcome.TIMED_OUT if gen.timed_out else ShitpostOutcome.FAILED_GENERATION
            )
            return ShitpostResult(outcome, reason=gen.error), "", None

        verdict = await workflow.execute_activity(
            check_guardrails_activity,
            gen.text,
            start_to_close_timeout=ACTIVITY_TIMEOUT,
        )
        if not verdict.allowed:
            return (
                ShitpostResult(ShitpostOutcome.BLOCKED_GUARDRAIL, reason=verdict.reason),
                gen.text,
                gen.whagent_session_id,
            )

        # the activity re-reads the gate immediately before posting
        posted = await workflow.execute_activity(
            post_and_record_shitpost_activity,
            PostParams(
                params=params,
                text=gen.text,
                persona=persona,
                whagent_session_id=gen.whagent_session_id,
                context_snapshot_id=snapshot_id,
            ),
            start_to_close_timeout=ACTIVITY_TIMEOUT,
            retry_policy=_ONCE,
        )
        return posted, gen.text, gen.whagent_session_id

    async def _record_skip(self, params: ShitpostParams, persona, snapshot_id) -> None:
        try:
            await workflow.execute_activity(
                record_scheduled_skip_activity,
                SkipParams(
                    channel_slack_id=params.channel_slack_id,
                    persona_id=persona.persona_id,
                    reason="writer_unavailable",
                    context_snapshot_id=snapshot_id,
                ),
                start_to_close_timeout=ACTIVITY_TIMEOUT,
                retry_policy=_ONCE,
            )
        except ActivityError:
            workflow.logger.error("failed to record the skipped scheduled slot")

    async def _notice(self, params: ShitpostParams) -> None:
        if not params.notice_slack_user_id:
            return
        try:
            await workflow.execute_activity(
                send_ephemeral_notice_activity,
                NoticeParams(
                    channel_slack_id=params.channel_slack_id,
                    slack_user_id=params.notice_slack_user_id,
                    text=NO_SHITPOST_NOTICE,
                    thread_ts=params.thread_ts,
                ),
                start_to_close_timeout=_NOTICE_TIMEOUT,
                retry_policy=_ONCE,
            )
        except ActivityError:
            workflow.logger.error("failed to send the no-shitpost notice")
