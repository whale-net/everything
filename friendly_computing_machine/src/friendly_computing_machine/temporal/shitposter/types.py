"""Shared types and constants for the Shitposter generation pipeline."""

import os
from dataclasses import dataclass
from datetime import timedelta
from typing import Optional

# Regenerations a scheduled post gets after a guardrail block before skipping.
MAX_SCHEDULED_ATTEMPTS = 3
# Hard bound on interactive (summon/riff) generation, inclusive of notice.
INTERACTIVE_DEADLINE = timedelta(seconds=60)
# Scheduled generation is not user-facing, so it gets a looser bound.
SCHEDULED_DEADLINE = timedelta(minutes=5)
ACTIVITY_TIMEOUT = timedelta(seconds=20)
NO_SHITPOST_NOTICE = "no shitpost this time"


class ShitpostOutcome:
    """Outcome string constants (plain str so Temporal's JSON converter round-trips them)."""


    POSTED = "posted"
    SKIPPED_GATE = "skipped_gate"
    BLOCKED_GUARDRAIL = "blocked_guardrail"
    FAILED_GENERATION = "failed_generation"
    TIMED_OUT = "timed_out"


@dataclass
class ShitpostParams:
    channel_slack_id: str
    # ShitposterTriggerEnum value: scheduled | summon | riff
    trigger: str
    # (iss, sub) of the linked human; both None means the FCM service subject
    # with no on_behalf_of.
    principal_iss: Optional[str] = None
    principal_sub: Optional[str] = None
    topic: Optional[str] = None
    # riff continuation: post in this thread on the existing session
    thread_ts: Optional[str] = None
    whagent_session_id: Optional[str] = None
    # recipient of the ephemeral "no shitpost" notice (summon/riff)
    notice_slack_user_id: Optional[str] = None
    thread_owner_slack_user_id: Optional[str] = None
    parent_post_id: Optional[int] = None


@dataclass
class ShitpostResult:
    outcome: str
    reason: Optional[str] = None
    slack_message_ts: Optional[str] = None
    whagent_session_id: Optional[str] = None


def load_shitposter_agent_id() -> str:
    """Read FCM_SHITPOSTER_AGENT_ID (the whagent agent holding a base prompt)."""
    agent_id = os.environ.get("FCM_SHITPOSTER_AGENT_ID", "")
    if not agent_id:
        raise RuntimeError("FCM_SHITPOSTER_AGENT_ID is not set")
    return agent_id


# Gap between scheduled posts, drawn uniformly per slot.
MIN_GAP = timedelta(hours=4)
MAX_GAP = timedelta(hours=12)
# Reserved from the upper gap so generation time keeps post-to-post under MAX_GAP.
GENERATION_MARGIN = timedelta(minutes=6)
# Silenced-state poll, a backstop for a missed resume signal.
SILENCE_POLL = timedelta(minutes=15)
# Slots per run before continue_as_new bounds history.
SLOTS_PER_RUN = 20


@dataclass
class ScheduleParams:
    channel_slack_id: str
    # slot counter carried across continue_as_new
    slot: int = 0
    # test/local override of the gap bounds; defaults are 4h-12h
    min_gap_seconds: float = MIN_GAP.total_seconds()
    max_gap_seconds: float = MAX_GAP.total_seconds()
    slots_per_run: int = SLOTS_PER_RUN
