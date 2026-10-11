"""Idea-generator brain job: keep generated lore near its standing-pool target.

Compute reads the persona's active lore, compares each generated kind's share
against its target weight, and asks that kind's agent for proposals only when
the share is below target. Each proposal is validated (citations, guardrails)
and the run cap applies across kinds. Apply writes the accepted entries and
their change-log rows in the runner's single transaction. Hit promotion is not
done here; lore_promotion owns it.
"""

import datetime
import json
import logging
import statistics
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal,
    shitposter_weight_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobStatus,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterLoreKindEnum,
    ShitposterMemoryCauseKindEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    idea_generator_run_cap,
    load_theme_generator_agent_id,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
    _member_names,
    _wait_for_reply,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    guardrails,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
    BrainJobInput,
    JobBody,
    register_job_body,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    get_whagent_client,
)

logger = logging.getLogger(__name__)

# Under the idea_generate job timeout in base.BRAIN_JOB_TIMEOUTS, so the agent deadline fires first.
AGENT_DEADLINE_SECONDS = 20 * 60
MAX_TEXT_CHARS = 280
MIN_THEME_CITATIONS = 2

# Rejection reasons specific to the idea-generator; guardrail reasons live in guardrails.py.
REASON_MALFORMED = "malformed"
REASON_INSUFFICIENT_CITATIONS = "insufficient_citations"
REASON_RUN_CAP = "run_cap"

NOVEL_GATE_NOT_IMPLEMENTED = "not_implemented"

THEME = ShitposterLoreKindEnum.THEME.value
NOVEL = ShitposterLoreKindEnum.NOVEL.value


@dataclass(frozen=True)
class TargetWeights:
    theme: float
    novel: float


def current_weights(persona_id: int) -> TargetWeights:
    """Stored target share per generated kind; the first read seeds and commits the starting weights."""
    rows = shitposter_weight_dal.get_current_weights(persona_id)
    return TargetWeights(theme=float(rows[THEME].weight), novel=float(rows[NOVEL].weight))


def pool_shares(active_kinds: list[str]) -> dict[str, float | None]:
    """Share of active lore per generated kind; None for an empty pool (counts as below target)."""
    if not active_kinds:
        return {THEME: None, NOVEL: None}
    total = len(active_kinds)
    return {k: sum(1 for a in active_kinds if a == k) / total for k in (THEME, NOVEL)}


def below_target(share: float | None, target: float) -> bool:
    return share is None or share < target


def median_hit_popularity(session: Session, persona_id: int) -> int:
    """Median popularity of active hit lore, or 0 when there is none."""
    scores = session.exec(
        select(ShitposterLoreEntry.popularity_score).where(
            ShitposterLoreEntry.persona_id == persona_id,
            ShitposterLoreEntry.kind == ShitposterLoreKindEnum.HIT.value,
            ShitposterLoreEntry.valid_to.is_(None),  # type: ignore[union-attr]
        )
    ).all()
    if not scores:
        return 0
    return round(statistics.median(scores))


def _theme_prompt(
    active: list[dict[str, Any]], retired: list[dict[str, Any]], max_entries: int
) -> str:
    return (
        f"Propose at most {max_entries} theme lore entries. Reply with only one JSON "
        'object: {"entries": [{"text": str, "cites": [lore_id, ...]}]}.\n\n'
        f"Active lore: {json.dumps(active)}\n"
        f"Retired by the operator: {json.dumps([r['text'] for r in retired])}"
    )


def _call_theme_agent(
    active: list[dict[str, Any]], retired: list[dict[str, Any]], max_entries: int
) -> str:
    client = get_whagent_client()
    deadline = time.monotonic() + AGENT_DEADLINE_SECONDS
    session = client.start_session(
        load_theme_generator_agent_id(),
        first_turn=_theme_prompt(active, retired, max_entries),
    )
    return _wait_for_reply(client, session.session_id, 0, deadline)


def _parse_entries(text: str) -> list[Any]:
    """The reply's entries list; a reply without one fails the run (per-entry problems are rejections)."""
    start, end = text.find("{"), text.rfind("}")
    if start < 0 or end < start:
        raise ValueError("generator reply has no JSON object")
    try:
        obj = json.loads(text[start : end + 1])
    except json.JSONDecodeError as e:
        raise ValueError(f"generator reply is not valid JSON: {e}") from e
    entries = obj.get("entries") if isinstance(obj, dict) else None
    if not isinstance(entries, list):
        raise ValueError("generator reply has no entries list")
    return entries


def _entry_text(raw: Any) -> str | None:
    text = raw.get("text") if isinstance(raw, dict) else None
    if not isinstance(text, str) or not text.strip() or len(text.strip()) > MAX_TEXT_CHARS:
        return None
    return text.strip()


def _cited_ids(raw: dict[str, Any], active_ids: set[int]) -> list[int]:
    """Distinct cited ids that are currently active lore, in citation order."""
    cites = raw.get("cites")
    if not isinstance(cites, list):
        return []
    out: list[int] = []
    for c in cites:
        try:
            lore_id = int(c)
        except (TypeError, ValueError):
            continue
        if lore_id in active_ids and lore_id not in out:
            out.append(lore_id)
    return out


def _validate_theme(
    raw_entries: list[Any],
    active_ids: set[int],
    member_names: list[str],
    retired: list[dict[str, Any]],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Accepted theme entries and rejections ({kind, text, reason})."""
    accepted: list[dict[str, Any]] = []
    rejections: list[dict[str, Any]] = []
    for raw in raw_entries:
        text = _entry_text(raw)
        if text is None:
            raw_text = raw.get("text") if isinstance(raw, dict) else None
            rejections.append(
                {"kind": THEME, "text": raw_text if isinstance(raw_text, str) else None, "reason": REASON_MALFORMED}
            )
            continue
        cited = _cited_ids(raw, active_ids)
        if len(cited) < MIN_THEME_CITATIONS:
            rejections.append({"kind": THEME, "text": text, "reason": REASON_INSUFFICIENT_CITATIONS})
            continue
        reason = guardrails.generated_text_refusal(text, member_names, retired)
        if reason is not None:
            rejections.append({"kind": THEME, "text": text, "reason": reason})
            continue
        accepted.append(
            {"kind": THEME, "text": text, "cited": cited, "agent_id": load_theme_generator_agent_id()}
        )
    return accepted, rejections


def _branch_reading(ctx: dict[str, Any], kind: str) -> dict[str, Any]:
    return {"share": ctx["shares"][kind], "target": getattr(ctx["weights"], kind)}


def _theme_branch(ctx: dict[str, Any]) -> dict[str, Any]:
    reading = _branch_reading(ctx, THEME)
    if not below_target(reading["share"], reading["target"]):
        return {"reading": reading, "accepted": [], "rejections": []}
    raw = _parse_entries(_call_theme_agent(ctx["active"], ctx["retired"], ctx["cap"]))
    accepted, rejections = _validate_theme(
        raw, ctx["active_ids"], ctx["member_names"], ctx["retired"]
    )
    return {"reading": reading, "accepted": accepted, "rejections": rejections}


def _novel_branch(ctx: dict[str, Any]) -> dict[str, Any]:
    """Novel exploration: gate not implemented yet, so it records the reading and emits nothing."""
    reading = _branch_reading(ctx, NOVEL)
    reading["gate"] = NOVEL_GATE_NOT_IMPLEMENTED
    return {"reading": reading, "accepted": [], "rejections": []}


# Generated kinds in dispatch order; earlier kinds take run-cap slots first.
_BRANCHES: list[tuple[str, Callable[[dict[str, Any]], dict[str, Any]]]] = [
    (THEME, _theme_branch),
    (NOVEL, _novel_branch),
]


def _enforce_run_cap(
    accepted: list[dict[str, Any]], rejections: list[dict[str, Any]], cap: int
) -> list[dict[str, Any]]:
    """Keep the first `cap` accepted entries; the rest are rejected as run_cap."""
    for dropped in accepted[cap:]:
        rejections.append({"kind": dropped["kind"], "text": dropped["text"], "reason": REASON_RUN_CAP})
    return accepted[:cap]


def _compute(params: BrainJobInput) -> dict[str, Any]:
    persona_id = params.persona_id
    cap = idea_generator_run_cap()
    weights = current_weights(persona_id)
    with SessionManager() as session:
        active_rows = shitposter_memory_dal.active_lore(persona_id, session=session)
        retired = guardrails.operator_retired_attributes(
            session, persona_id
        ) + guardrails.operator_retired_lore(session, persona_id)
    member_names = _member_names()

    # the session is closed before any agent call so no transaction spans it
    ctx = {
        "persona_id": persona_id,
        "active": [{"id": r.id, "text": r.text, "kind": r.kind} for r in active_rows],
        "active_ids": {r.id for r in active_rows},
        "retired": retired,
        "member_names": member_names,
        "shares": pool_shares([r.kind for r in active_rows]),
        "weights": weights,
        "cap": cap,
    }
    readings: dict[str, dict[str, Any]] = {}
    accepted: list[dict[str, Any]] = []
    rejections: list[dict[str, Any]] = []
    for kind, branch in _BRANCHES:
        result = branch(ctx)
        readings[kind] = result["reading"]
        accepted += result["accepted"]
        rejections += result["rejections"]
    accepted = _enforce_run_cap(accepted, rejections, cap)
    logger.info(
        "idea_generate computed persona=%s active=%d accepted=%d rejected=%d",
        persona_id,
        len(active_rows),
        len(accepted),
        len(rejections),
    )
    return {
        "persona_id": persona_id,
        THEME: readings[THEME],
        NOVEL: readings[NOVEL],
        "proposals": accepted,
        "rejections": rejections,
    }


def _cause_ref(cited: list[int]) -> str:
    return ",".join(f"lore:{i}" for i in cited)


def _apply(session: Session, run_id: int, payload: dict[str, Any]) -> ApplyOutcome:
    persona_id = payload["persona_id"]
    now = datetime.datetime.now(datetime.timezone.utc)
    rejections: list[dict[str, Any]] = list(payload["rejections"])
    entries: list[dict[str, Any]] = []
    proposals: list[dict[str, Any]] = payload["proposals"]
    if proposals:
        popularity = median_hit_popularity(session, persona_id)
        active_ids = {
            r.id for r in shitposter_memory_dal.active_lore(persona_id, session=session)
        }
    for p in proposals:
        # a cited entry may have been retired between compute and apply
        cited = [i for i in p["cited"] if i in active_ids]
        if p["kind"] == THEME and len(cited) < MIN_THEME_CITATIONS:
            rejections.append({"kind": THEME, "text": p["text"], "reason": REASON_INSUFFICIENT_CITATIONS})
            continue
        row = shitposter_memory_dal._add_lore(
            session,
            persona_id=persona_id,
            text=p["text"],
            kind=p["kind"],
            cause_kind=ShitposterMemoryCauseKindEnum.THEME_SYNTHESIS.value,
            source_post_id=None,
            cause_post_id=None,
            cause_ref=_cause_ref(cited),
            reflector_run_id=None,
            popularity_score=popularity,
            now=now,
        )
        entries.append(
            {"lore_id": row.id, "kind": p["kind"], "agent_id": p["agent_id"], "cited": cited}
        )
    session.flush()

    details = {
        THEME: payload[THEME],
        NOVEL: payload[NOVEL],
        "entries": entries,
        "rejections": rejections,
        "refreshes": [],
        "weight_changes": [],
    }
    status = ShitposterBrainJobStatus.SUCCEEDED if entries else ShitposterBrainJobStatus.NO_OP
    logger.info(
        "idea_generate applied run=%s status=%s entries=%d rejected=%d",
        run_id,
        status.value,
        len(entries),
        len(rejections),
    )
    return ApplyOutcome(status=status.value, details=details)


register_job_body(
    ShitposterBrainJobKind.IDEA_GENERATE,
    JobBody(compute=_compute, apply=_apply),
)
