"""Slack entry points for starting/continuing a whagent-net agent session.

app_mention starts a new per-thread relay workflow (SlackThreadAgentWorkflow,
temporal/whagent/workflow.py). A plain message reply in an already-tracked
thread signals that same workflow rather than starting a new one -- see
get_thread_session's fast "is this thread active" check below.

relay_thread_reply is not a Bolt listener itself: Bolt dispatches only the
first matching listener per event, so events.py's single "message" listener
calls it.
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
from friendly_computing_machine.src.friendly_computing_machine.db.dal.identity_dal import (
    get_keycloak_identity,
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


def _slack_team_id(event) -> str:
    """The workspace team id for an app_mention event.

    Slack's app_mention payload carries the team as team_id (string) on the
    event, with team sometimes present as a string and sometimes as a dict --
    accept both and ignore anything that isn't a usable team id, so a
    missing/odd team just falls through to the unlinked (unset) behaviour.
    """
    team_id = event.get("team_id")
    if isinstance(team_id, str) and team_id:
        return team_id
    team = event.get("team")
    if isinstance(team, str) and team:
        return team
    if isinstance(team, dict):
        nested = team.get("id")
        if isinstance(nested, str) and nested:
            return nested
    return ""


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

            # A mention inside an already-active thread is just another reply;
            # the "message" event for it is relayed by relay_thread_reply.
            existing = get_thread_session(slack_channel.id, thread_ts)
            if (
                existing is not None
                and existing.status == SlackThreadSessionStatusEnum.ACTIVE
            ):
                span.set_attribute("whagent.thread_already_active", True)
                return

            first_message = _strip_bot_mention(event.get("text", ""))
            client = get_whagent_client()

            # A linked user's session runs on behalf of their Keycloak
            # identity so whagent-net (and downstream MCP servers) see the
            # human, not the bot. Unlinked users keep the default unset
            # behaviour; the Slack user never holds a credential -- the
            # asserted subject is only data on fcm's service-credential call.
            slack_user_id = event.get("user", "")
            identity = get_keycloak_identity(
                _slack_team_id(event), slack_user_id
            )
            if identity is not None:
                span.set_attribute("whagent.on_behalf_of", True)

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
                    slack_user_id=slack_user_id,
                    whagent_ui_public_url=client.ui_public_url,
                    on_behalf_of_iss=(
                        identity.keycloak_iss if identity is not None else None
                    ),
                    on_behalf_of_sub=(
                        identity.keycloak_sub if identity is not None else None
                    ),
                ),
                id=workflow_id,
                task_queue=get_temporal_queue_name("main"),
            )
            span.set_attribute("temporal.workflow.started", True)
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            raise


def relay_thread_reply(event) -> bool:
    """Signal the thread's relay workflow if this message belongs to an active one.

    Returns True if the message was relayed.
    """
    with tracer.start_as_current_span("relay_whagent_thread_reply") as span:
        try:
            # Only plain user messages are turns -- edits, joins, bot
            # posts (including the relay's own placeholder/status
            # messages) never are.
            if event.get("subtype") is not None or event.get("bot_id"):
                return False

            thread_ts = event.get("thread_ts")
            if not thread_ts:
                return False  # not a threaded reply

            user_id = event.get("user")
            if user_id is None:
                return False
            config = get_bot_config()
            if user_id in config.BOT_SLACK_USER_IDS:
                return False

            channel_slack_id = event["channel"]
            slack_channel = get_slack_channel(slack_channel_slack_id=channel_slack_id)
            if slack_channel is None:
                return False

            thread_session = get_thread_session(slack_channel.id, thread_ts)
            if (
                thread_session is None
                or thread_session.status != SlackThreadSessionStatusEnum.ACTIVE
            ):
                return False

            text = _strip_bot_mention(event.get("text", ""))
            workflow_id = workflow_id_for_thread(
                get_app_env(), channel_slack_id, thread_ts
            )
            span.set_attribute("temporal.workflow.id", workflow_id)

            signal_workflow(workflow_id, SlackThreadAgentWorkflow.queue_message, text)
            span.set_attribute("whagent.signal.sent", True)
            return True
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            raise
