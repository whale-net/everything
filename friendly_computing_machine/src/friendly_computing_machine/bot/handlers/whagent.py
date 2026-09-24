"""Slack entry points for starting/continuing a whagent-net agent session.

app_mention starts a new per-thread relay workflow (SlackThreadAgentWorkflow,
temporal/whagent/workflow.py). A plain message reply in an already-tracked
thread signals that same workflow rather than starting a new one -- see
get_thread_session's fast "is this thread active" check below.
"""

import logging
import re

from opentelemetry import trace

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    app,
    get_agent_id_for_channel,
    get_bot_config,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    get_slack_channel,
    get_thread_session,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackThreadSessionStatusEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    get_app_env,
    get_temporal_queue_name,
    signal_workflow,
    start_workflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.workflow import (
    SlackThreadAgentWorkflow,
    SlackThreadAgentWorkflowParams,
    workflow_id_for_thread,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    get_whagent_client,
)

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)

# Slack renders a mention as "<@U12345> actual text" at the start of the
# message -- strip exactly that leading token, not every mention in the text.
_LEADING_MENTION_RE = re.compile(r"^\s*<@[^>]+>\s*")


def _strip_bot_mention(text: str) -> str:
    return _LEADING_MENTION_RE.sub("", text, count=1).strip()


@app.event("app_mention")
def handle_whagent_app_mention(event, say):
    with tracer.start_as_current_span("handle_whagent_app_mention") as span:
        try:
            channel_slack_id = event["channel"]
            thread_ts = event.get("thread_ts") or event["ts"]
            span.set_attribute("slack.channel.id", channel_slack_id)
            span.set_attribute("slack.thread_ts", thread_ts)

            slack_channel = get_slack_channel(slack_channel_slack_id=channel_slack_id)
            agent_id = (
                get_agent_id_for_channel(slack_channel.id)
                if slack_channel is not None
                else None
            )
            if agent_id is None:
                span.set_attribute("whagent.agent_configured", False)
                say(
                    text="No whagent-net agent is configured for this channel.",
                    thread_ts=thread_ts,
                )
                return
            span.set_attribute("whagent.agent_id", agent_id)

            first_message = _strip_bot_mention(event.get("text", ""))
            client = get_whagent_client()

            workflow_id = workflow_id_for_thread(
                get_app_env(), channel_slack_id, thread_ts
            )
            span.set_attribute("temporal.workflow.id", workflow_id)

            start_workflow(
                SlackThreadAgentWorkflow.run,
                SlackThreadAgentWorkflowParams(
                    channel_slack_id=channel_slack_id,
                    channel_db_id=slack_channel.id,
                    thread_ts=thread_ts,
                    agent_id=agent_id,
                    first_message=first_message,
                    slack_user_id=event.get("user", ""),
                    whagent_ui_public_url=client.ui_public_url,
                ),
                id=workflow_id,
                task_queue=get_temporal_queue_name("main"),
            )
            span.set_attribute("temporal.workflow.started", True)
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            raise


@app.event("message")
def handle_whagent_thread_reply(event, say):
    with tracer.start_as_current_span("handle_whagent_thread_reply") as span:
        try:
            # Only plain user messages are turns -- edits, joins, bot
            # posts (including the relay's own placeholder/status
            # messages) never are.
            if event.get("subtype") is not None or event.get("bot_id"):
                return

            thread_ts = event.get("thread_ts")
            if not thread_ts:
                return  # not a threaded reply

            user_id = event.get("user")
            if user_id is None:
                return
            config = get_bot_config()
            if user_id in config.BOT_SLACK_USER_IDS:
                return

            channel_slack_id = event["channel"]
            slack_channel = get_slack_channel(slack_channel_slack_id=channel_slack_id)
            if slack_channel is None:
                return

            thread_session = get_thread_session(slack_channel.id, thread_ts)
            if (
                thread_session is None
                or thread_session.status != SlackThreadSessionStatusEnum.ACTIVE
            ):
                return

            text = event.get("text", "")
            workflow_id = workflow_id_for_thread(
                get_app_env(), channel_slack_id, thread_ts
            )
            span.set_attribute("temporal.workflow.id", workflow_id)

            signal_workflow(workflow_id, SlackThreadAgentWorkflow.queue_message, text)
            span.set_attribute("whagent.signal.sent", True)
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            raise
