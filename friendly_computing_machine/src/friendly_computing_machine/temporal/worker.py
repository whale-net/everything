import asyncio
import logging
from concurrent.futures import ThreadPoolExecutor

from temporalio.worker import Worker
from temporalio.worker.workflow_sandbox import (
    SandboxedWorkflowRunner,
    SandboxRestrictions,
)

from friendly_computing_machine.src.friendly_computing_machine.temporal.ai.activity import (
    detect_call_to_action,
    generate_gemini_response,
    generate_summary,
    get_vibe,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.base import (
    AbstractScheduleWorkflow,
)
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
from friendly_computing_machine.src.friendly_computing_machine.temporal.sample import (
    SayHello,
    build_hello_prompt,
    say_hello,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.slack.activity import (
    backfill_slack_user_info_activity,
    fix_slack_tagging_activity,
    generate_context_prompt,
    get_slack_channel_context,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.slack.workflow import (
    SlackContextGeminiWorkflow,
    SlackMessageQODWorkflow,
    SlackUserInfoWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    get_temporal_client_async,
    get_temporal_queue_name,
)

logger = logging.getLogger(__name__)


WORKFLOWS = [
    SayHello,
    SlackContextGeminiWorkflow,
    SlackMessageQODWorkflow,
    SlackUserInfoWorkflow,
]
ACTIVITIES = [
    generate_context_prompt,
    get_vibe,
    get_slack_channel_context,
    say_hello,
    generate_gemini_response,
    build_hello_prompt,
    generate_summary,
    detect_call_to_action,
    backfill_slack_messages_slack_user_id_activity,
    backfill_slack_messages_slack_channel_id_activity,
    backfill_slack_messages_slack_team_id_activity,
    backfill_slack_user_info_activity,
    backfill_teams_from_messages_activity,
    delete_slack_message_duplicates_activity,
    upsert_slack_user_creates_activity,
    backfill_genai_text_slack_user_id_activity,
    backfill_genai_text_slack_channel_id_activity,
    fix_slack_tagging_activity,
]


async def run_worker(app_env: str):
    # Create client connected to server at the given address
    client = await get_temporal_client_async()

    # create schedules for schedule workflows
    futures = []
    for wf in WORKFLOWS:
        if not issubclass(wf, AbstractScheduleWorkflow):
            continue
        futures.append(wf().async_upsert_schedule(client, app_env))
    await asyncio.gather(*futures)
    logger.info("all schedules created")

    # Run the worker
    with ThreadPoolExecutor(max_workers=100) as activity_executor:
        runner = SandboxedWorkflowRunner(
            # restrictions=SandboxRestrictions.default.with_passthrough_modules("slack_sdk"),
            # TODO - actually ifgure out what is not deterministic
            # it is slack calls, but from where?
            restrictions=SandboxRestrictions.default.with_passthrough_all_modules(),
        )
        worker = Worker(
            client,
            task_queue=get_temporal_queue_name("main"),
            workflows=WORKFLOWS,
            activities=ACTIVITIES,
            activity_executor=activity_executor,
            workflow_runner=runner,
        )
        await worker.run()
