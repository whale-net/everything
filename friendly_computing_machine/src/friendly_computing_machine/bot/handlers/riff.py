"""Thread riffs under a Shitposter post: the thread owner riffs with the bot.

Dispatched from events.py's single "message" listener. A reply whose thread
root is not a shitposter post is not ours and returns False unchanged.
"""

import logging
import uuid

from opentelemetry import trace

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    get_slack_web_client,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers.whagent import (
    _slack_team_id,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.identity_link import (
    prompt_identity_link,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    get_slack_channel,
    shitposter_gate,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.identity_dal import (
    get_keycloak_identity,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.shitposter_dal import (
    get_latest_riff_session_id,
    get_post_by_channel_ts,
    set_thread_owner_if_unset,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
    ShitpostParams,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.workflow import (
    ShitpostWorkflow,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    get_app_env,
    get_temporal_queue_name,
    start_workflow,
)

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)

POINTER_TEXT = "Only the thread owner can riff here. Start your own with /shitpost."
# Slack subtypes that are still a person's reply in the thread.
_HUMAN_SUBTYPES = (None, "", "thread_broadcast")
_CONTEXT_MESSAGES = 20
_CONTEXT_CHARS = 2000


def _thread_context(client, channel_id: str, thread_ts: str) -> str | None:
    """Recent thread text for a new session's first turn; None if unavailable."""
    try:
        resp = client.conversations_replies(
            channel=channel_id, ts=thread_ts, limit=_CONTEXT_MESSAGES
        )
        lines = [m.get("text", "").strip() for m in resp.get("messages", [])]
    except Exception:
        logger.warning("could not read thread context channel=%s", channel_id)
        return None
    text = "\n".join(line for line in lines if line)
    return text[-_CONTEXT_CHARS:] or None


def _start_riff(params: ShitpostParams) -> None:
    start_workflow(
        ShitpostWorkflow.run,
        params,
        id=f"shitpost-{get_app_env()}-riff-{params.channel_slack_id}-{uuid.uuid4().hex}",
        task_queue=get_temporal_queue_name("main"),
    )


def riff_thread_reply(event, client=None) -> bool:
    """Handle a human reply under a shitposter post; True if the thread is one.

    Returns False for anything that is not a human reply to a shitposter
    post, leaving the event to the rest of the message pipeline.
    """
    with tracer.start_as_current_span("riff_thread_reply") as span:
        if event.get("bot_id") or event.get("subtype") not in _HUMAN_SUBTYPES:
            return False
        thread_ts = event.get("thread_ts")
        user_id = event.get("user")
        if not thread_ts or thread_ts == event.get("ts") or not user_id:
            return False

        channel_id = event["channel"]
        slack_channel = get_slack_channel(slack_channel_slack_id=channel_id)
        if slack_channel is None:
            return False
        root = get_post_by_channel_ts(slack_channel.id, thread_ts)
        if root is None:
            return False
        span.set_attribute("shitposter.thread_root_post_id", root.id)

        # uncached: a silence or opt-out takes effect on the very next reply
        if not shitposter_gate(channel_id).allowed:
            span.set_attribute("shitposter.riff.gated", True)
            return True

        client = client or get_slack_web_client()
        team_id = _slack_team_id(event)
        identity = get_keycloak_identity(team_id, user_id)

        owner = root.thread_owner_slack_user_id
        claimed = False
        # first linked replier of a scheduled post claims the thread; the DB decides a tie
        if owner is None and root.trigger == "scheduled" and identity is not None:
            claimed = set_thread_owner_if_unset(root.id, user_id)
            if claimed:
                owner = user_id
            else:
                root = get_post_by_channel_ts(slack_channel.id, thread_ts)
                owner = root.thread_owner_slack_user_id
        span.set_attribute("shitposter.riff.claimed", claimed)

        if identity is None:
            prompt_identity_link(client, channel_id, team_id, user_id, "Shitposter")
            return True
        if owner != user_id:
            client.chat_postEphemeral(
                channel=channel_id, user=user_id, thread_ts=thread_ts, text=POINTER_TEXT
            )
            return True

        # summons continue the summon's session; a scheduled thread's riffs
        # run on the owner's own session, never the service-subject one
        session_id = get_latest_riff_session_id(slack_channel.id, thread_ts)
        if session_id is None and root.trigger == "summon":
            session_id = root.whagent_session_id
        context = None
        if session_id is None:
            context = _thread_context(client, channel_id, thread_ts)

        _start_riff(
            ShitpostParams(
                channel_slack_id=channel_id,
                trigger="riff",
                principal_iss=identity.keycloak_iss,
                principal_sub=identity.keycloak_sub,
                topic=(event.get("text") or "").strip() or None,
                thread_ts=thread_ts,
                whagent_session_id=session_id,
                notice_slack_user_id=user_id,
                thread_owner_slack_user_id=owner,
                parent_post_id=root.id,
                thread_context=context,
            )
        )
        span.set_attribute("shitposter.riff.started", True)
        span.set_attribute("shitposter.riff.new_session", session_id is None)
        return True
