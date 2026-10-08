"""Operator surface for the Shitposter persona: change history, run rejections,
retire, and per-post snapshot lookup. Admin-only; output is ephemeral.
"""

import datetime
from dataclasses import dataclass
from typing import Optional

OPERATOR_USAGE = (
    "Usage: `/shitposter history [n]`, `/shitposter runs [n]`, "
    "`/shitposter retire <attribute|lore> <id>`, `/shitposter snapshot <post permalink>`"
)
OPERATOR_SUBCOMMANDS = frozenset({"history", "runs", "retire", "snapshot"})
DEFAULT_PAGE_SIZE = 20
MAX_PAGE_SIZE = 100


@dataclass(frozen=True)
class ChangeCause:
    """Resolved cause of one memory change, rendered for the operator."""

    kind: str  # "post" | "suggestion" | "operator"
    detail: str  # engagement summary, backer names (operator-only), or subject
    occurred_at: Optional[datetime.datetime] = None


def is_operator(slack_user_id: str, admin_ids: frozenset[str]) -> bool:
    """True when the caller is in the FCM admin set (the M6 check)."""
    raise NotImplementedError


def handle_operator_command(
    text: str,
    caller_slack_user_id: str,
    admin_ids: frozenset[str],
) -> str:
    """Parse a `/shitposter` operator subcommand and return the ephemeral reply."""
    raise NotImplementedError


def describe_cause(change_id: int) -> ChangeCause:
    """Join memory_change to engagement rows or suggestion backers; never empty."""
    raise NotImplementedError


def render_history(persona_id: int, limit: int) -> str:
    """Attribute and lore changes, newest first: time, operation, before/after, cause."""
    raise NotImplementedError


def render_runs(persona_id: int, limit: int) -> str:
    """Reflector run records with rejection count and reasons."""
    raise NotImplementedError


def retire_entry(
    entity_kind: str,
    entity_id: int,
    operator_subject: str,
) -> str:
    """Retire an active attribute or lore entry with cause_kind=operator."""
    raise NotImplementedError


def render_snapshot_for_post(post_permalink: str) -> str:
    """Snapshot the post was written from; reports when the snapshot is NULL."""
    raise NotImplementedError
