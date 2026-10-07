"""Client-side control of per-channel schedule workflows (start, stop, resume)."""

import asyncio
import logging
import os

from temporalio.service import RPCError, RPCStatusCode

from friendly_computing_machine.src.friendly_computing_machine.db import dal
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.schedule_workflow import (
    ShitposterChannelScheduleWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
    ScheduleParams,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    get_app_env,
    get_temporal_client_async,
    get_temporal_queue_name,
)

logger = logging.getLogger(__name__)

GAP_MIN_ENV = "FCM_SHITPOSTER_GAP_MIN_SECONDS"
GAP_MAX_ENV = "FCM_SHITPOSTER_GAP_MAX_SECONDS"


def schedule_workflow_id(channel_slack_id: str, app_env: str | None = None) -> str:
    return f"fcm-{app_env or get_app_env()}-shitposter-schedule-{channel_slack_id}"


def schedule_params(channel_slack_id: str) -> ScheduleParams:
    """Default 4-12h gaps; the env pair shrinks them for local runs."""
    params = ScheduleParams(channel_slack_id=channel_slack_id)
    if os.environ.get(GAP_MIN_ENV) and os.environ.get(GAP_MAX_ENV):
        params.min_gap_seconds = float(os.environ[GAP_MIN_ENV])
        params.max_gap_seconds = float(os.environ[GAP_MAX_ENV])
    return params


async def ensure_schedule_async(client, channel_slack_id: str, task_queue: str, app_env: str | None = None) -> None:
    """Idempotent start-or-re-enable: signal-with-start, so a running run is never duplicated.

    The start signal is a no-op on a running, unstopped run; after an opt-out it
    re-enables the parked run with a fresh gap.
    """
    await client.start_workflow(
        ShitposterChannelScheduleWorkflow.run,
        schedule_params(channel_slack_id),
        id=schedule_workflow_id(channel_slack_id, app_env),
        task_queue=task_queue,
        start_signal="start",
    )


async def _signal(client, channel_slack_id: str, name: str, app_env: str | None = None) -> None:
    handle = client.get_workflow_handle(schedule_workflow_id(channel_slack_id, app_env))
    try:
        await handle.signal(name)
    except RPCError as e:
        # no running schedule for this channel: nothing to signal
        if e.status != RPCStatusCode.NOT_FOUND:
            raise


async def reconcile_schedules_async(client, task_queue: str, app_env: str | None = None) -> None:
    """Ensure a schedule workflow for every currently opted-in channel."""
    for channel in dal.list_opted_in_channel_slack_ids():
        await ensure_schedule_async(client, channel, task_queue, app_env)
    logger.info("shitposter schedules reconciled")


async def handle_state_change_async(event: str, channel_slack_id: str | None) -> None:
    client = await get_temporal_client_async()
    queue = get_temporal_queue_name("main")
    if event == "optin" and channel_slack_id:
        await ensure_schedule_async(client, channel_slack_id, queue)
    elif event == "optout" and channel_slack_id:
        await _signal(client, channel_slack_id, "stop")
    elif event == "resume":
        for channel in dal.list_opted_in_channel_slack_ids():
            await ensure_schedule_async(client, channel, queue)
            await _signal(client, channel, "resume")


def notify_schedule_state_change(event: str, channel_slack_id: str | None) -> None:
    """DAL notifier (sync): runs the Temporal calls to completion."""
    asyncio.run(handle_state_change_async(event, channel_slack_id))
