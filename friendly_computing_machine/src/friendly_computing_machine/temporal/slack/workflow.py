import asyncio
import logging
from dataclasses import dataclass
from datetime import timedelta

from temporalio import workflow
from temporalio.client import ScheduleIntervalSpec, ScheduleSpec

from friendly_computing_machine.src.friendly_computing_machine.temporal.ai.activity import (
    detect_call_to_action,
    generate_gemini_response,
    generate_summary,
    get_vibe,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.base import AbstractScheduleWorkflow
from friendly_computing_machine.src.friendly_computing_machine.temporal.db.job_activity import (
    backfill_genai_text_slack_channel_id_activity,
    backfill_genai_text_slack_user_id_activity,
    backfill_slack_messages_slack_channel_id_activity,
    backfill_slack_messages_slack_team_id_activity,
    backfill_slack_messages_slack_user_id_activity,
    backfill_teams_from_messages_activity,
    delete_slack_message_duplicates_activity,
    upsert_slack_user_creates_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.slack.activity import (
    FixSlackTaggingParams,
    GenerateContextPromptParams,
    backfill_slack_user_info_activity,
    fix_slack_tagging_activity,
    generate_context_prompt,
    get_slack_channel_context,
)

logger = logging.getLogger(__name__)


@dataclass
class SlackContextGeminiWorkflowParams:
    """
    Parameters for the SlackContextGeminiWorkflow.
    """

    slack_channel_slack_id: str
    prompt: str


@workflow.defn
class SlackContextGeminiWorkflow:
    """
    Workflow to generate a response using the Gemini AI model.
    This workflow is specifically designed to work with the Slack context.
    It retrieves the previous messages from the Slack channel and generates a response
    based on the provided prompt and the summarized context.
    """

    @workflow.run
    async def run(self, params: SlackContextGeminiWorkflowParams):
        slack_prompts = await workflow.execute_activity(
            get_slack_channel_context,
            params.slack_channel_slack_id,
            schedule_to_close_timeout=timedelta(seconds=5),
            start_to_close_timeout=timedelta(seconds=5),
        )
        summary_future = workflow.execute_activity(
            generate_summary,
            slack_prompts,
            schedule_to_close_timeout=timedelta(seconds=10),
            start_to_close_timeout=timedelta(seconds=10),
        )
        vibe_future = workflow.execute_activity(
            get_vibe,
            params.prompt,
            start_to_close_timeout=timedelta(seconds=10),
        )

        vibe, summary = await asyncio.gather(vibe_future, summary_future)

        # this is a hack to do something I don't know how to do
        # and doesn't really work, but it's at least a start
        # TODO - understand context
        context_prompt = await workflow.execute_activity(
            generate_context_prompt,
            GenerateContextPromptParams(params.prompt, summary, vibe),
            schedule_to_close_timeout=timedelta(seconds=10),
            start_to_close_timeout=timedelta(seconds=10),
        )
        response = await workflow.execute_activity(
            generate_gemini_response,
            context_prompt,
            schedule_to_close_timeout=timedelta(seconds=10),
            start_to_close_timeout=timedelta(seconds=10),
        )

        # Detect if this response contains a call to action
        is_call_to_action = await workflow.execute_activity(
            detect_call_to_action,
            response,
            schedule_to_close_timeout=timedelta(seconds=10),
            start_to_close_timeout=timedelta(seconds=10),
        )

        tagged_response = await workflow.execute_activity(
            fix_slack_tagging_activity,
            FixSlackTaggingParams(response, is_call_to_action),
            schedule_to_close_timeout=timedelta(seconds=5),
            start_to_close_timeout=timedelta(seconds=5),
        )

        return tagged_response


@dataclass
class SlackMessageQODWorkflowParams:
    """
    Parameters for the SlackMessageQODWorkflow.
    """

    pass


@workflow.defn
class SlackMessageQODWorkflow(AbstractScheduleWorkflow):
    """
    Workflow to generate a response using the Gemini AI model.
    This workflow is specifically designed to work with the Slack context.
    It retrieves the previous messages from the Slack channel and generates a response
    based on the provided prompt and the summarized context.
    """

    def get_schedule_spec(self) -> ScheduleSpec:
        return ScheduleSpec(
            intervals=[ScheduleIntervalSpec(every=timedelta(minutes=2))],
        )

    @workflow.run
    async def run(
        self, params: SlackContextGeminiWorkflowParams = SlackMessageQODWorkflowParams()
    ):
        logger.info("params %s", params)

        await workflow.start_activity(
            backfill_teams_from_messages_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )

        channel_activity = workflow.start_activity(
            backfill_slack_messages_slack_channel_id_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )
        user_activity = workflow.start_activity(
            backfill_slack_messages_slack_user_id_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )
        team_activity = workflow.start_activity(
            backfill_slack_messages_slack_team_id_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )
        delete_activity = workflow.start_activity(
            delete_slack_message_duplicates_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )

        logger.info("waiting for activities to finish")
        results = await asyncio.gather(
            channel_activity,
            user_activity,
            team_activity,
            delete_activity,
        )
        logger.info("results %s", results)

        # execute genai tasks after slack tasks because they are dependent on the ids being updated
        genai_user_id = workflow.execute_activity(
            backfill_genai_text_slack_user_id_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )
        genai_channel_id = workflow.execute_activity(
            backfill_genai_text_slack_channel_id_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )
        genai_results = await asyncio.gather(
            genai_user_id,
            genai_channel_id,
        )
        logger.info("genai results %s", genai_results)

        return results, genai_results


class SlackUserInfoWorkflowParams:
    """
    Parameters for the SlackUserInfoWorkflow.
    """

    pass


@workflow.defn
class SlackUserInfoWorkflow(AbstractScheduleWorkflow):
    def get_schedule_spec(self) -> ScheduleSpec:
        # 30 minutes should be enough time for this job
        # don't want to spam slack API, but also don't want to wait too long on updates
        return ScheduleSpec(
            intervals=[ScheduleIntervalSpec(every=timedelta(minutes=30))],
        )

    @workflow.run
    async def run(
        self, params: SlackUserInfoWorkflowParams = SlackUserInfoWorkflowParams()
    ):
        logger.debug("params %s", params)
        # TODO - storing creates here is less than ideal as it will store them in temporal
        # these should be stored elsewhere and retrieved as needed
        creates = await workflow.execute_activity(
            backfill_slack_user_info_activity,
            start_to_close_timeout=timedelta(seconds=30),
        )
        await workflow.execute_activity(
            upsert_slack_user_creates_activity,
            creates,
            start_to_close_timeout=timedelta(seconds=30),
        )
        await workflow.execute_activity(
            backfill_slack_messages_slack_user_id_activity,
            start_to_close_timeout=timedelta(seconds=10),
        )

        return "OK"
