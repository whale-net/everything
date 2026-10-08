"""Operator surface for the Shitposter persona: change history, run rejections,
retire, and per-post snapshot lookup. Admin-only; output is ephemeral.

Also rendered by `fcm tools shitposter` so long output can be paged from a shell.
"""

import datetime
import logging
import re
from dataclasses import dataclass
from typing import Optional

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as memory_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_snapshot_dal as snapshot_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_dal import (
    get_slack_channel,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterMemoryCauseKindEnum,
    ShitposterMemoryChange,
    ShitposterMemoryEntityKindEnum,
)

logger = logging.getLogger(__name__)

OPERATOR_USAGE = (
    "Usage: `/shitpost admin history [n]`, `/shitpost admin runs [n]`, "
    "`/shitpost admin retire <attribute|lore> <id>`, "
    "`/shitpost admin snapshot <post permalink>`"
)
OPERATOR_SUBCOMMANDS = frozenset({"history", "runs", "retire", "snapshot"})
NOT_ADMIN = "Only Shitposter admins can run admin commands."
NO_PERSONA = "No Shitposter persona is seeded yet."
DEFAULT_PAGE_SIZE = 20
MAX_PAGE_SIZE = 100

_PERMALINK = re.compile(r"/archives/([A-Z0-9]+)/p(\d{7,})")
_SNAPSHOT_TEXT_CAP = 3500


@dataclass(frozen=True)
class ChangeCause:
    """Resolved cause of one memory change, rendered for the operator."""

    kind: str  # "post_engagement" | "suggestion" | "operator" | "fold"
    detail: str  # engagement summary, backer names (operator-only), or subject
    occurred_at: Optional[datetime.datetime] = None


def is_operator(slack_user_id: str, admin_ids: frozenset[str]) -> bool:
    """True when the caller is in the FCM admin set (the M6 check)."""
    return slack_user_id in admin_ids


def _operator_subject(slack_user_id: str) -> str:
    return f"slack:{slack_user_id}"


def _when(value: Optional[datetime.datetime]) -> str:
    if value is None:
        return "unknown time"
    if value.tzinfo is None:
        value = value.replace(tzinfo=datetime.UTC)
    return value.astimezone(datetime.UTC).strftime("%Y-%m-%d %H:%M UTC")


def _parse_limit(args: list[str]) -> int:
    if not args:
        return DEFAULT_PAGE_SIZE
    if len(args) != 1 or not args[0].isdigit():
        raise ValueError("limit must be a number")
    return max(1, min(int(args[0]), MAX_PAGE_SIZE))


def _default_persona_id() -> int | None:
    persona = shitposter_dal.get_default_persona()
    return persona.id if persona is not None else None


def handle_operator_command(
    text: str,
    caller_slack_user_id: str,
    admin_ids: frozenset[str],
) -> str:
    """Parse a `/shitpost admin` operator subcommand and return the ephemeral reply."""
    if not is_operator(caller_slack_user_id, admin_ids):
        return NOT_ADMIN
    parts = text.strip().split()
    if not parts or parts[0].lower() not in OPERATOR_SUBCOMMANDS:
        return OPERATOR_USAGE
    sub, args = parts[0].lower(), parts[1:]

    if sub in ("history", "runs"):
        try:
            limit = _parse_limit(args)
        except ValueError:
            return OPERATOR_USAGE
        persona_id = _default_persona_id()
        if persona_id is None:
            return NO_PERSONA
        if sub == "history":
            return render_history(persona_id, limit)
        return render_runs(persona_id, limit)

    if sub == "retire":
        if len(args) != 2 or args[0].lower() not in (
            ShitposterMemoryEntityKindEnum.ATTRIBUTE.value,
            ShitposterMemoryEntityKindEnum.LORE.value,
        ) or not args[1].isdigit():
            return OPERATOR_USAGE
        return retire_entry(
            args[0].lower(), int(args[1]), _operator_subject(caller_slack_user_id)
        )

    # snapshot
    if not args:
        return OPERATOR_USAGE
    return render_snapshot_for_post(" ".join(args))


def _cause_refs(change: ShitposterMemoryChange) -> list[str]:
    refs = [r.strip() for r in (change.cause_ref or "").split(",") if r.strip()]
    post_ref = f"post:{change.cause_post_id}"
    if change.cause_post_id is not None and post_ref not in refs:
        refs.insert(0, post_ref)
    return refs


def _describe_post(post_id: int) -> tuple[str, Optional[datetime.datetime]]:
    engagement = memory_dal.get_post_engagement(post_id)
    if engagement is None:
        return f"bot post {post_id} (engagement not finalized)", None
    emoji = ", ".join(
        f":{name}: x{count}"
        for name, count in sorted(engagement.reactions_by_emoji.items())
    )
    text = (
        f"bot post {post_id}: {engagement.distinct_reactors} reactors "
        f"({emoji or 'none'}), {engagement.distinct_repliers} repliers, "
        f"{engagement.negative_reactions} negative"
    )
    return text, engagement.finalized_at


def _describe_suggestion(suggestion_id: int) -> tuple[str, Optional[datetime.datetime]]:
    suggestion = memory_dal.get_suggestion(suggestion_id)
    if suggestion is None:
        return f"suggestion {suggestion_id} (row missing)", None
    # the submitter counts once toward the threshold without a backer row
    backers = [suggestion.submitter_slack_user_id]
    for slack_user_id in memory_dal.active_backer_slack_ids(suggestion_id):
        if slack_user_id not in backers:
            backers.append(slack_user_id)
    people = ", ".join(f"<@{u}>" for u in backers)
    text = (
        f"suggestion {suggestion_id} \"{suggestion.text}\" "
        f"backed by {len(backers)}: {people}"
    )
    return text, suggestion.promoted_at


def describe_cause(change: ShitposterMemoryChange) -> ChangeCause:
    """Join memory_change to engagement rows or suggestion backers; never empty."""
    kind = change.cause_kind
    if kind == ShitposterMemoryCauseKindEnum.OPERATOR.value:
        return ChangeCause(kind, f"operator {change.cause_ref}", change.changed_at)
    if kind == ShitposterMemoryCauseKindEnum.FOLD.value:
        return ChangeCause(kind, f"fold {change.cause_ref}", change.changed_at)

    parts: list[str] = []
    occurred: Optional[datetime.datetime] = None
    for ref in _cause_refs(change):
        head, _, ident = ref.partition(":")
        if head == "post" and ident.isdigit():
            text, when = _describe_post(int(ident))
        elif head == "suggestion" and ident.isdigit():
            text, when = _describe_suggestion(int(ident))
        else:
            text, when = ref, None
        parts.append(text)
        occurred = occurred or when
    if not parts:
        return ChangeCause(kind, f"{kind} (no source recorded)", change.changed_at)
    return ChangeCause(kind, "; ".join(parts), occurred or change.changed_at)


def _render_change(change: ShitposterMemoryChange) -> str:
    cause = describe_cause(change)
    lines = [
        f"#{change.id} {_when(change.changed_at)} · {change.operation} {change.entity_kind}"
    ]
    if change.before_text is not None:
        lines.append(f'  before: "{change.before_text}"')
    if change.after_text is not None:
        lines.append(f'  after: "{change.after_text}"')
    run = f" (reflector run {change.reflector_run_id})" if change.reflector_run_id else ""
    lines.append(f"  cause: {cause.detail}{run}")
    return "\n".join(lines)


def render_history(persona_id: int, limit: int, offset: int = 0) -> str:
    """Attribute and lore changes, newest first: time, operation, before/after, cause."""
    changes = []
    for change in memory_dal.change_history(persona_id):
        if change.cause_post_id is None and not change.cause_ref:
            logger.warning("memory change %s has no cause; not rendered", change.id)
            continue
        changes.append(change)
    total = len(changes)
    page = changes[offset : offset + limit]
    if not page:
        return f"No changes at offset {offset} ({total} total)."
    header = f"Changes {offset + 1}-{offset + len(page)} of {total}, newest first."
    return "\n".join([header, *(_render_change(c) for c in page)])


def _render_run(run, started_at: datetime.datetime) -> str:
    head = (
        f"#{run.brain_job_run_id} {_when(started_at)} · inputs {run.input_count}, "
        f"carried {run.carried_over_count}, applied {run.applied_count}, "
        f"rejected {run.rejection_count}"
    )
    lines = [head]
    for rejection in run.rejections or []:
        op = rejection.get("op", "?")
        reason = rejection.get("reason", "?")
        lines.append(f"  rejected {op}: {reason}")
    return "\n".join(lines)


def render_runs(persona_id: int, limit: int, offset: int = 0) -> str:
    """Reflector runs with rejection count and reasons, newest first."""
    total = memory_dal.count_reflector_runs(persona_id)
    rows = memory_dal.list_reflector_runs(persona_id, limit, offset)
    if not rows:
        return f"No reflector runs at offset {offset} ({total} total)."
    header = f"Reflector runs {offset + 1}-{offset + len(rows)} of {total}, newest first."
    return "\n".join([header, *(_render_run(run, started) for run, started in rows)])


def retire_entry(
    entity_kind: str,
    entity_id: int,
    operator_subject: str,
) -> str:
    """Retire an active attribute or lore entry with cause_kind=operator."""
    try:
        change = memory_dal.operator_retire(entity_kind, entity_id, operator_subject)
    except (LookupError, ValueError) as exc:
        return f"Retire failed: {exc}."
    before = f': "{change.before_text}"' if change.before_text else ""
    return (
        f"Retired {entity_kind} {entity_id}{before}. Logged change #{change.id} "
        f"with cause operator {operator_subject}."
    )


def parse_permalink(url: str) -> tuple[str, str] | None:
    """Slack message permalink -> (channel slack id, message ts), or None."""
    cleaned = url.strip().strip("<>").split("|", 1)[0]
    match = _PERMALINK.search(cleaned)
    if match is None:
        return None
    digits = match.group(2)
    return match.group(1), f"{digits[:-6]}.{digits[-6:]}"


def render_snapshot_for_post(post_permalink: str) -> str:
    """Snapshot the post was written from; reports when the snapshot is NULL."""
    ref = parse_permalink(post_permalink)
    if ref is None:
        return "That is not a Slack message permalink."
    channel = get_slack_channel(slack_channel_slack_id=ref[0])
    post = (
        shitposter_dal.get_post_by_channel_ts(channel.id, ref[1])
        if channel is not None
        else None
    )
    if post is None:
        return "No Shitposter post matches that permalink."
    if post.context_snapshot_id is None:
        latest = snapshot_dal.latest_snapshot(post.persona_id)
        tail = f" Latest snapshot is v{latest.version}." if latest is not None else ""
        return (
            f"Post {post.id} has no recorded snapshot "
            f"(written before snapshots were recorded).{tail}"
        )
    snapshot = snapshot_dal.snapshot_by_id(post.context_snapshot_id)
    if snapshot is None:
        return f"Post {post.id} points at snapshot {post.context_snapshot_id}, which is missing."
    text = snapshot.rendered_text
    if len(text) > _SNAPSHOT_TEXT_CAP:
        text = text[:_SNAPSHOT_TEXT_CAP] + " (truncated)"
    return (
        f"Post {post.id} was written from snapshot {snapshot.id} "
        f"(v{snapshot.version}, {snapshot.token_count} tokens, {_when(snapshot.created_at)}):\n"
        f"```{text}```"
    )


def page_history(page: int, page_size: int) -> str:
    """CLI paging: 1-based page over the persona's change history."""
    persona_id = _default_persona_id()
    if persona_id is None:
        return NO_PERSONA
    size = max(1, min(page_size, MAX_PAGE_SIZE))
    return render_history(persona_id, size, (max(page, 1) - 1) * size)


def page_runs(page: int, page_size: int) -> str:
    """CLI paging: 1-based page over the persona's reflector runs."""
    persona_id = _default_persona_id()
    if persona_id is None:
        return NO_PERSONA
    size = max(1, min(page_size, MAX_PAGE_SIZE))
    return render_runs(persona_id, size, (max(page, 1) - 1) * size)
