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
import os
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
    mint_link_token,
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


def _web_public_url() -> str:
    # base URL of the identity-link web app; the minted one-time token is
    # appended as /link/<token>, which the web app redeems.
    return os.environ.get("FCM_WEB_PUBLIC_URL", "").rstrip("/")


def _slack_team_id(event, body) -> str:
    """Workspace id for an app_mention.

    The inner app_mention payload carries `team`; the event envelope carries
    `team_id`. Prefer the event, fall back to the envelope.
    """
    return event.get("team") or (body or {}).get("team_id") or ""


@app.event("app_mention")
def handle_whagent_app_mention(event, say, client=None, body=None):
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

            # Block-until-linked gate. A mention from a user with no stored
            # Slack->Keycloak mapping gets a one-time link prompt instead of a
            # session, checked before any workflow / slackthreadsession row is
            # created so a blocked mention leaves no orphaned ACTIVE thread.
            team_id = _slack_team_id(event, body)
            user_id = event.get("user")
            if user_id and get_keycloak_identity(team_id, user_id) is None:
                web_public_url = _web_public_url()
                if not web_public_url:
                    logger.error(
                        "FCM_WEB_PUBLIC_URL is unset; cannot mint identity link "
                        "for unlinked slack team=%s user=%s",
                        team_id,
                        user_id,
                    )
                else:
                    token = mint_link_token(team_id, user_id)
                    client.chat_postEphemeral(
                        channel=channel_slack_id,
                        user=user_id,
                        thread_ts=thread_ts,
                        text=(
                            "Link your Slack account to use this agent: "
                            f"<{web_public_url}/link/{token}|Link my account> "
                            "(one-time link, expires in 10 minutes)."
                        ),
                    )
                    logger.info(
                        "issued identity link prompt slack team=%s user=%s",
                        team_id,
                        user_id,
                    )
                span.set_attribute("whagent.identity_link_prompted", True)
                return

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
            whagent_client = get_whagent_client()

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
                    whagent_ui_public_url=whagent_client.ui_public_url,
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
