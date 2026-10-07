"""`/shitpost` slash command: admin control plane and the summon hook."""

import datetime
import logging

from opentelemetry import trace
from slack_bolt import Ack, Respond, Say

from friendly_computing_machine.src.friendly_computing_machine.bot.app import app
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    insert_slack_command,
    is_channel_opted_in,
    is_shitposter_enabled,
    set_channel_opt_in,
    set_kill_switch,
    set_state_change_notifier,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackCommandCreate,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    load_admin_slack_user_ids,
)

from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.schedule_control import (
    notify_schedule_state_change,
)

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)

ADMIN_SUBCOMMAND = "admin"
ADMIN_USAGE = (
    "Usage: `/shitpost admin optin|optout|silence [reason]|resume|status`"
)

# opt-in/out and resume drive the per-channel schedule workflows
set_state_change_notifier(notify_schedule_state_change)

# read once at startup
_ADMIN_SLACK_USER_IDS = load_admin_slack_user_ids()


def handle_shitpost(
    command: dict, admin_ids: frozenset[str]
) -> tuple[str, str | None]:
    """Run the command; returns (ephemeral reply, visible channel notice or None)."""
    user_id = command["user_id"]
    channel_id = command["channel_id"]
    parts = (command.get("text") or "").strip().split(maxsplit=2)

    if not parts or parts[0].lower() != ADMIN_SUBCOMMAND:
        # summon path lands with the summon task
        return "Summoning a shitpost is not available yet.", None

    if user_id not in admin_ids:
        return "Only Shitposter admins can run admin commands.", None

    sub = parts[1].lower() if len(parts) > 1 else ""
    rest = parts[2].strip() if len(parts) > 2 else None
    if sub in ("optin", "optout"):
        opted_in = sub == "optin"
        changed = set_channel_opt_in(
            channel_id,
            opted_in,
            user_id,
            command.get("team_id"),
            channel_name=command.get("channel_name"),
        )
        state = "on" if opted_in else "off"
        if not changed:
            return f"Shitposter is already {state} in this channel.", None
        notice = (
            "Shitposter is now on here. While it is on, messages and reactions "
            "in this channel are captured."
            if opted_in
            else "Shitposter is now off here."
        )
        return f"Shitposter is now {state} in this channel.", notice
    if sub == "silence":
        changed = set_kill_switch(False, user_id, rest)
        return (
            "Shitposter silenced everywhere."
            if changed
            else "Shitposter was already silenced."
        ), None
    if sub == "resume":
        changed = set_kill_switch(True, user_id, rest)
        return (
            "Shitposter resumed."
            if changed
            else "Shitposter was not silenced."
        ), None
    if sub == "status":
        workspace = "on" if is_shitposter_enabled() else "silenced"
        channel = "on" if is_channel_opted_in(channel_id) else "off"
        return f"Workspace: {workspace}. This channel: {channel}.", None
    return ADMIN_USAGE, None


@app.command("/shitpost")
def handle_shitpost_command(ack: Ack, respond: Respond, say: Say, command):
    with tracer.start_as_current_span("handle_shitpost_command") as span:
        ack()
        span.set_attribute("slack.command", "/shitpost")
        span.set_attribute("slack.user.id", command["user_id"])
        span.set_attribute("slack.channel.id", command["channel_id"])
        insert_slack_command(
            SlackCommandCreate(
                caller_slack_user_id=command["user_id"],
                command_base="/shitpost",
                command_text=command.get("text", ""),
                slack_channel_slack_id=command["channel_id"],
                created_at=datetime.datetime.now(),
            )
        )
        reply, notice = handle_shitpost(command, _ADMIN_SLACK_USER_IDS)
        respond(text=reply, response_type="ephemeral")
        if notice:
            say(text=notice)
