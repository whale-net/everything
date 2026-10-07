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
from friendly_computing_machine.src.friendly_computing_machine.temporal.db.music_poll_activity import (
    get_music_poll_channels_activity,
    get_scheduled_poll_run_activity,
    post_scheduled_poll_activity,
    record_scheduled_poll_run_activity,
    select_music_poll_options_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.music_poll.workflow import (
    WeeklyMusicPollWorkflow,
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
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
    check_guardrails_activity,
    expire_pending_suggestions_activity,
    generate_shitpost_activity,
    post_and_record_shitpost_activity,
    resolve_persona_activity,
    send_ephemeral_notice_activity,
    shitposter_gate_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.schedule_control import (
    reconcile_schedules_async,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.schedule_workflow import (
    ShitposterChannelScheduleWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.suggestion_sweep_workflow import (
    ShitposterSuggestionSweepWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.workflow import (
    ShitpostWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.activity import (
    get_whagent_session_activity,
    insert_thread_session_activity,
    post_slack_thread_message_activity,
    read_whagent_transcript_activity,
    send_whagent_turn_activity,
    start_whagent_session_activity,
    update_slack_message_activity,
    update_thread_session_status_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.workflow import (
    SlackThreadAgentWorkflow,
)

logger = logging.getLogger(__name__)


WORKFLOWS = [
    SayHello,
    SlackContextGeminiWorkflow,
    SlackMessageQODWorkflow,
    SlackUserInfoWorkflow,
    SlackThreadAgentWorkflow,
    WeeklyMusicPollWorkflow,
    ShitpostWorkflow,
    ShitposterChannelScheduleWorkflow,
    ShitposterSuggestionSweepWorkflow,
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
    get_music_poll_channels_activity,
    get_scheduled_poll_run_activity,
    post_scheduled_poll_activity,
    record_scheduled_poll_run_activity,
    select_music_poll_options_activity,
    start_whagent_session_activity,
    send_whagent_turn_activity,
    get_whagent_session_activity,
    read_whagent_transcript_activity,
    insert_thread_session_activity,
    update_thread_session_status_activity,
    post_slack_thread_message_activity,
    update_slack_message_activity,
    shitposter_gate_activity,
    resolve_persona_activity,
    generate_shitpost_activity,
    check_guardrails_activity,
    post_and_record_shitpost_activity,
    send_ephemeral_notice_activity,
    expire_pending_suggestions_activity,
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
    try:
        await reconcile_schedules_async(client, get_temporal_queue_name("main"), app_env)
    except Exception:
        logger.exception("shitposter schedule reconciliation failed")

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
