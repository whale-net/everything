"""Operator surface for the Shitposter persona: change history, run rejections,
active lore by source, idea-generator runs, retire, and per-post snapshot lookup. Admin-only; output is ephemeral.

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
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_weight_dal as weight_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_dal import (
    get_slack_channel,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobRun,
    ShitposterBrainJobStatus,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    NovelGateReading,
    ShitposterLoreKindEnum,
    ShitposterMemoryCauseKindEnum,
    ShitposterMemoryChange,
    ShitposterMemoryEntityKindEnum,
    ShitposterWeightSourceEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    load_source_weight_config,
)

logger = logging.getLogger(__name__)

OPERATOR_USAGE = (
    "Usage: `/shitpost admin history [n]`, `/shitpost admin runs [n]`, "
    "`/shitpost admin lore [hit|consolidated|theme|novel] [n]`, "
    "`/shitpost admin ideas [n]`, `/shitpost admin weights [n]`, "
    "`/shitpost admin retire <attribute|lore> <id>`, "
    "`/shitpost admin snapshot <post permalink>`"
)
OPERATOR_SUBCOMMANDS = frozenset(
    {"history", "runs", "lore", "ideas", "weights", "retire", "snapshot"}
)
LORE_SOURCES = tuple(k.value for k in ShitposterLoreKindEnum)
NOT_ADMIN = "Only Shitposter admins can run admin commands."
NO_PERSONA = "No Shitposter persona is seeded yet."
DEFAULT_PAGE_SIZE = 20
MAX_PAGE_SIZE = 100

_PERMALINK = re.compile(r"/archives/([A-Z0-9]+)/p(\d{7,})")
_SNAPSHOT_TEXT_CAP = 3500


@dataclass(frozen=True)
class ChangeCause:
    """Resolved cause of one memory change, rendered for the operator."""

    kind: str  # "post_engagement" | "suggestion" | "operator" | "fold" | "theme_synthesis" | "novel_gate"
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

    if sub == "lore":
        source = None
        if args and args[0].lower() in LORE_SOURCES:
            source, args = args[0].lower(), args[1:]
        try:
            limit = _parse_limit(args)
        except ValueError:
            return OPERATOR_USAGE
        persona_id = _default_persona_id()
        if persona_id is None:
            return NO_PERSONA
        return render_lore(persona_id, source, limit)

    if sub in ("ideas", "weights"):
        try:
            limit = _parse_limit(args)
        except ValueError:
            return OPERATOR_USAGE
        persona_id = _default_persona_id()
        if persona_id is None:
            return NO_PERSONA
        if sub == "ideas":
            return render_idea_runs(persona_id, limit)
        return render_weight_history(persona_id, limit)

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


def _describe_theme_synthesis(change: ShitposterMemoryChange) -> str:
    """'theme: generalizes lore #a, #b' from the cited lore refs."""
    cited = []
    for ref in _cause_refs(change):
        head, _, ident = ref.partition(":")
        cited.append(f"#{ident}" if head == "lore" and ident.isdigit() else ref)
    if not cited:
        return "theme: generalizes lore (no citations recorded)"
    return f"theme: generalizes lore {', '.join(cited)}"


def _fmt_num(value: float) -> str:
    return f"{value:.2f}".rstrip("0").rstrip(".") or "0"


def _describe_novel_gate(change: ShitposterMemoryChange) -> str:
    """'novel: recent mean X < F x baseline mean Y'; the raw ref when it does not parse."""
    reading = NovelGateReading.from_cause_ref(change.cause_ref)
    if reading is None:
        return f"novel: {change.cause_ref or '(no gate reading recorded)'}"
    return (
        f"novel: recent mean {_fmt_num(reading.recent_mean)} < "
        f"{_fmt_num(reading.fraction)} x baseline mean {_fmt_num(reading.baseline_mean)}"
    )


def describe_cause(change: ShitposterMemoryChange) -> ChangeCause:
    """Join memory_change to engagement rows or suggestion backers; never empty."""
    kind = change.cause_kind
    if kind == ShitposterMemoryCauseKindEnum.OPERATOR.value:
        return ChangeCause(kind, f"operator {change.cause_ref}", change.changed_at)
    if kind == ShitposterMemoryCauseKindEnum.FOLD.value:
        return ChangeCause(kind, f"fold {change.cause_ref}", change.changed_at)
    if kind == ShitposterMemoryCauseKindEnum.THEME_SYNTHESIS.value:
        return ChangeCause(kind, _describe_theme_synthesis(change), change.changed_at)
    if kind == ShitposterMemoryCauseKindEnum.NOVEL_GATE.value:
        return ChangeCause(kind, _describe_novel_gate(change), change.changed_at)

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


def _render_lore_entry(entry) -> str:
    return (
        f"#{entry.id} [{entry.kind}] popularity {entry.popularity_score} · "
        f"since {_when(entry.valid_from)}\n  \"{entry.text}\""
    )


def render_lore(
    persona_id: int, source: Optional[str], limit: int, offset: int = 0
) -> str:
    """Active lore with each entry's source, optionally filtered to one source."""
    total = memory_dal.count_lore_for_operator(persona_id, source)
    rows = memory_dal.list_lore_for_operator(persona_id, source, limit, offset)
    scope = f"{source} lore" if source else "lore"
    weights = render_source_weights(persona_id)
    if not rows:
        return f"No active {scope} at offset {offset} ({total} total).\n{weights}"
    header = (
        f"Active {scope} {offset + 1}-{offset + len(rows)} of {total}, "
        "by source then id."
    )
    return "\n".join([header, *(_render_lore_entry(e) for e in rows), weights])


def render_source_weights(persona_id: int) -> str:
    """Current theme/novel target weights with their bounds and the combined ceiling."""
    cfg = load_source_weight_config()
    rows = weight_dal.get_current_weights(persona_id, config=cfg)
    bounds = {
        ShitposterWeightSourceEnum.THEME.value: (cfg.theme_min, cfg.theme_max),
        ShitposterWeightSourceEnum.NOVEL.value: (cfg.novel_min, cfg.novel_max),
    }
    parts = []
    for source, (lo, hi) in bounds.items():
        row = rows.get(source)
        current = "unset" if row is None else _pct(float(row.weight))
        parts.append(f"{source} {current} (bounds {_pct(lo)}-{_pct(hi)})")
    return f"Source weights: {', '.join(parts)}; combined ceiling {_pct(cfg.ceiling)}."


def _pct(value: float) -> str:
    return f"{value * 100:g}%"


def _render_rows(label: str, rows: list, render_row) -> list[str]:
    """One line per row under `label`; an empty list renders as 'none'."""
    if not rows:
        return [f"  {label}: none"]
    return [f"  {label}:", *(f"    {render_row(row)}" for row in rows)]


def _render_generic_row(row) -> str:
    if isinstance(row, dict):
        return ", ".join(f"{k} {v}" for k, v in row.items())
    return str(row)


def _render_entry_row(row: dict) -> str:
    cited = row.get("cited") or []
    cites = f", cites {', '.join(f'#{c}' for c in cited)}" if cited else ""
    return (
        f"lore #{row.get('lore_id', '?')} {row.get('kind', '?')} "
        f"by agent {row.get('agent_id', '?')}{cites}"
    )


def _render_rejection_row(row: dict) -> str:
    text = row.get("text")
    quoted = f' "{text}"' if text else ""
    return f"{row.get('kind', '?')}{quoted}: {row.get('reason', '?')}"


def _render_share(label: str, reading) -> str:
    if not isinstance(reading, dict):
        return f"{label} n/a"
    share = reading.get("share")
    shown = "n/a (empty pool)" if share is None else _fmt_num(float(share))
    target = reading.get("target")
    target_shown = "?" if target is None else _fmt_num(float(target))
    return f"{label} share {shown} / target {target_shown}"


def _render_gate(novel) -> str:
    """Novel gate as open or closed; a not-yet-computed gate counts as closed."""
    gate = novel.get("gate") if isinstance(novel, dict) else None
    if gate is None:
        return "novel gate: not computed"
    if isinstance(gate, dict):
        state = "open" if gate.get("open") else "closed"
        rest = {k: v for k, v in gate.items() if k != "open"}
        detail = f" ({_render_generic_row(rest)})" if rest else ""
        return f"novel gate: {state}{detail}"
    if gate in ("open", "closed"):
        return f"novel gate: {gate}"
    return f"novel gate: closed ({gate})"


def _idea_run_reason(run: ShitposterBrainJobRun, details: dict) -> str:
    status = run.status
    if status == ShitposterBrainJobStatus.SKIPPED.value:
        return run.skip_reason or "no reason recorded"
    if status == ShitposterBrainJobStatus.FAILED.value:
        return run.error or "no error recorded"
    entries = details.get("entries") or []
    if status == ShitposterBrainJobStatus.SUCCEEDED.value:
        reason = f"wrote {len(entries)} entr{'y' if len(entries) == 1 else 'ies'}"
        stepped = [
            w for w in details.get("weight_changes") or []
            if isinstance(w, dict) and "new" in w
        ]
        if stepped:
            reason += f", stepped {len(stepped)} weight{'' if len(stepped) == 1 else 's'}"
        return reason
    if status == ShitposterBrainJobStatus.NO_OP.value:
        rejections = details.get("rejections") or []
        if rejections:
            return f"every proposal rejected ({len(rejections)})"
        return "shares at or above target"
    return status


def _render_idea_run(run: ShitposterBrainJobRun) -> str:
    """Status and reason, shares vs target, novel gate, entries, rejections,
    refreshes and weight changes from the run's details."""
    details = run.details if isinstance(run.details, dict) else {}
    lines = [
        f"#{run.id} {_when(run.started_at)} · {run.status} ({run.trigger}): "
        f"{_idea_run_reason(run, details)}",
        f"  {_render_share('theme', details.get('theme'))} · "
        f"{_render_share('novel', details.get('novel'))}",
        f"  {_render_gate(details.get('novel'))}",
    ]
    lines += _render_rows("entries", details.get("entries") or [], _render_entry_row)
    lines += _render_rows(
        "rejections", details.get("rejections") or [], _render_rejection_row
    )
    lines += _render_rows(
        "refreshes", details.get("refreshes") or [], _render_generic_row
    )
    lines += _render_rows(
        "weight changes", details.get("weight_changes") or [], _render_generic_row
    )
    return "\n".join(lines)


def render_idea_runs(persona_id: int, limit: int, offset: int = 0) -> str:
    """Idea-generator runs, newest first."""
    kind = ShitposterBrainJobKind.IDEA_GENERATE.value
    total = memory_dal.count_brain_job_runs(persona_id, kind)
    rows = memory_dal.list_brain_job_runs(persona_id, kind, limit, offset)
    if not rows:
        return f"No idea-generator runs at offset {offset} ({total} total)."
    header = (
        f"Idea-generator runs {offset + 1}-{offset + len(rows)} of {total}, newest first."
    )
    return "\n".join([header, *(_render_idea_run(r) for r in rows)])


def _render_weight_cause(cause) -> str:
    if not isinstance(cause, dict) or not cause:
        return "no cause recorded"
    if "with_mean" in cause:
        return (
            f"with lore mean {cause.get('with_mean')} (n {cause.get('with_n')}) vs "
            f"without mean {cause.get('without_mean')} (n {cause.get('without_n')}) "
            f"over {cause.get('days_compared')} days"
        )
    return _render_generic_row(cause)


def _render_weight_revision(row) -> str:
    run = f" · run #{row.brain_job_run_id}" if row.brain_job_run_id else ""
    state = "current" if row.valid_to is None else f"until {_when(row.valid_to)}"
    return (
        f"#{row.id} {row.source} {_pct(float(row.weight))} · since {_when(row.valid_from)} "
        f"({state}){run}\n  cause: {_render_weight_cause(row.cause)}"
    )


def render_weight_history(persona_id: int, limit: int, offset: int = 0) -> str:
    """Theme/novel weight revisions with their causes, newest first."""
    rows = list(reversed(weight_dal.list_weight_history(persona_id)))
    total = len(rows)
    page = rows[offset : offset + limit]
    if not page:
        return f"No weight revisions at offset {offset} ({total} total)."
    header = (
        f"Weight revisions {offset + 1}-{offset + len(page)} of {total}, newest first."
    )
    return "\n".join([header, *(_render_weight_revision(r) for r in page)])


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


def page_lore(source: Optional[str], page: int, page_size: int) -> str:
    """CLI paging: 1-based page over the persona's active lore."""
    if source is not None:
        source = source.lower()
        if source not in LORE_SOURCES:
            return f"Unknown source {source!r}; use one of {', '.join(LORE_SOURCES)}."
    persona_id = _default_persona_id()
    if persona_id is None:
        return NO_PERSONA
    size = max(1, min(page_size, MAX_PAGE_SIZE))
    return render_lore(persona_id, source, size, (max(page, 1) - 1) * size)


def page_idea_runs(page: int, page_size: int) -> str:
    """CLI paging: 1-based page over the persona's idea-generator runs."""
    persona_id = _default_persona_id()
    if persona_id is None:
        return NO_PERSONA
    size = max(1, min(page_size, MAX_PAGE_SIZE))
    return render_idea_runs(persona_id, size, (max(page, 1) - 1) * size)


def page_weight_history(page: int, page_size: int) -> str:
    """CLI paging: 1-based page over the persona's source-weight revisions."""
    persona_id = _default_persona_id()
    if persona_id is None:
        return NO_PERSONA
    size = max(1, min(page_size, MAX_PAGE_SIZE))
    return render_weight_history(persona_id, size, (max(page, 1) - 1) * size)
