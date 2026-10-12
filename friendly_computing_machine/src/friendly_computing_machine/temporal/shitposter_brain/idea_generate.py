"""Idea-generator brain job: keep generated lore near its standing-pool target.

Compute first steps the theme/novel target weights from how posts with each
source's lore performed (source_weights), so the stepped weights drive this
run's targeting; apply writes those weight revisions only when the run does not
fail. Compute then reads the persona's active lore, compares each generated kind's share
against its target weight, and asks that kind's agent for proposals only when
the share is below target; novel is asked only while the novel gate reads a
relative engagement drop. Each proposal is validated (citations, guardrails)
and the run cap applies across kinds. Apply writes the accepted entries and
their change-log rows in the runner's single transaction, then refreshes generated
lore's earned popularity (lore_refresh). Hit promotion is not done here;
lore_promotion owns it.
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
from temporalio.client import Client

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
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    NovelGateReading,
    ShitposterLoreEntry,
    ShitposterLoreKindEnum,
    ShitposterMemoryCauseKindEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    idea_generator_cadence_hours,
    idea_generator_run_cap,
    load_novel_generator_agent_id,
    load_theme_generator_agent_id,
    novel_gate_baseline_days,
    novel_gate_fraction,
    novel_gate_recent_days,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
    _wait_for_reply,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    guardrails,
    lore_refresh,
    source_weights,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
    BrainJobInput,
    JobBody,
    register_job_body,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.control import (
    register_brain_schedule,
    remove_brain_schedule,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.lore_promotion import (
    popularity_score,
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

# Novel gate reasons recorded in the run's novel.gate reading.
GATE_REASON_OPEN = "engagement_drop"
GATE_REASON_NO_DROP = "no_engagement_drop"
GATE_REASON_NO_ENGAGEMENT = "no_engagement_in_window"

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


@dataclass(frozen=True)
class NovelGate:
    """One run's novel-gate reading; means are None when their window has no finalized engagement."""

    open: bool
    recent_mean: float | None
    baseline_mean: float | None
    fraction: float
    reason: str

    def as_details(self) -> dict[str, Any]:
        return {
            "open": self.open,
            "recent_mean": self.recent_mean,
            "baseline_mean": self.baseline_mean,
            "fraction": self.fraction,
            "reason": self.reason,
        }


def _utc(dt: datetime.datetime) -> datetime.datetime:
    # sqlite returns naive datetimes; stored timestamps are UTC
    return dt if dt.tzinfo is not None else dt.replace(tzinfo=datetime.timezone.utc)


def finalized_mean_popularity(
    session: Session,
    persona_id: int,
    since: datetime.datetime,
    until: datetime.datetime | None = None,
) -> float | None:
    """Mean popularity of the persona's posts whose engagement finalized at or after `since`."""
    rows = session.exec(
        select(
            ShitposterPostEngagement.distinct_reactors,
            ShitposterPostEngagement.distinct_repliers,
            ShitposterPostEngagement.finalized_at,
        ).where(ShitposterPostEngagement.persona_id == persona_id)
    ).all()
    scores = [
        popularity_score(reactors, repliers)
        for reactors, repliers, finalized_at in rows
        if _utc(finalized_at) >= since and (until is None or _utc(finalized_at) <= until)
    ]
    if not scores:
        return None
    return statistics.fmean(scores)


def read_novel_gate(
    session: Session, persona_id: int, now: datetime.datetime
) -> NovelGate:
    """Open only when the recent-window mean is below fraction x the baseline-window mean."""
    fraction = novel_gate_fraction()
    recent = finalized_mean_popularity(
        session, persona_id, now - datetime.timedelta(days=novel_gate_recent_days()), now
    )
    baseline = finalized_mean_popularity(
        session, persona_id, now - datetime.timedelta(days=novel_gate_baseline_days()), now
    )
    if recent is None or baseline is None:
        return NovelGate(False, recent, baseline, fraction, GATE_REASON_NO_ENGAGEMENT)
    if recent < fraction * baseline:
        return NovelGate(True, recent, baseline, fraction, GATE_REASON_OPEN)
    return NovelGate(False, recent, baseline, fraction, GATE_REASON_NO_DROP)


def _novel_prompt(
    active: list[dict[str, Any]],
    retired: list[dict[str, Any]],
    max_entries: int,
    attributes: list[dict[str, Any]],
) -> str:
    # engagement figures stay out of the prompt so a slump nudges rather than swings the voice
    return (
        f"Propose at most {max_entries} novel lore entries: fresh running bits that fit "
        'this persona. Reply with only one JSON object: {"entries": [{"text": str}]}.\n\n'
        f"Persona attributes: {json.dumps(attributes)}\n"
        f"Active lore: {json.dumps(active)}\n"
        f"Retired by the operator: {json.dumps([r['text'] for r in retired])}"
    )


def _call_novel_agent(
    active: list[dict[str, Any]],
    retired: list[dict[str, Any]],
    max_entries: int,
    attributes: list[dict[str, Any]],
) -> str:
    client = get_whagent_client()
    deadline = time.monotonic() + AGENT_DEADLINE_SECONDS
    session = client.start_session(
        load_novel_generator_agent_id(),
        first_turn=_novel_prompt(active, retired, max_entries, attributes),
    )
    return _wait_for_reply(client, session.session_id, 0, deadline)


def _validate_novel(
    raw_entries: list[Any],
    member_names: list[str],
    retired: list[dict[str, Any]],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Accepted novel entries and rejections ({kind, text, reason}); no citation requirement."""
    accepted: list[dict[str, Any]] = []
    rejections: list[dict[str, Any]] = []
    for raw in raw_entries:
        text = _entry_text(raw)
        if text is None:
            raw_text = raw.get("text") if isinstance(raw, dict) else None
            rejections.append(
                {"kind": NOVEL, "text": raw_text if isinstance(raw_text, str) else None, "reason": REASON_MALFORMED}
            )
            continue
        reason = guardrails.generated_text_refusal(text, member_names, retired)
        if reason is not None:
            rejections.append({"kind": NOVEL, "text": text, "reason": reason})
            continue
        accepted.append(
            {"kind": NOVEL, "text": text, "cited": [], "agent_id": load_novel_generator_agent_id()}
        )
    return accepted, rejections


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


def _theme_branch(ctx: dict[str, Any], slots: int) -> dict[str, Any]:
    reading = _branch_reading(ctx, THEME)
    if slots <= 0 or not below_target(reading["share"], reading["target"]):
        return {"reading": reading, "accepted": [], "rejections": []}
    raw = _parse_entries(_call_theme_agent(ctx["active"], ctx["retired"], slots))
    accepted, rejections = _validate_theme(
        raw, ctx["active_ids"], ctx["member_names"], ctx["retired"]
    )
    return {"reading": reading, "accepted": accepted, "rejections": rejections}


def _novel_branch(ctx: dict[str, Any], slots: int) -> dict[str, Any]:
    """Novel exploration, only while the gate reads an engagement drop and the share is below target."""
    reading = _branch_reading(ctx, NOVEL)
    reading["gate"] = ctx["gate"].as_details()
    if (
        slots <= 0
        or not ctx["gate"].open
        or not below_target(reading["share"], reading["target"])
    ):
        return {"reading": reading, "accepted": [], "rejections": []}
    raw = _parse_entries(_call_novel_agent(ctx["active"], ctx["retired"], slots, ctx["attributes"]))
    accepted, rejections = _validate_novel(raw, ctx["member_names"], ctx["retired"])
    return {"reading": reading, "accepted": accepted, "rejections": rejections}


# Generated kinds in dispatch order; earlier kinds take run-cap slots first.
_BRANCHES: list[tuple[str, Callable[[dict[str, Any], int], dict[str, Any]]]] = [
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
    seeded = current_weights(persona_id)
    now = datetime.datetime.now(datetime.timezone.utc)
    with SessionManager() as session:
        weight_decisions = [
            d.as_payload()
            for d in source_weights.compute_weight_changes(session, persona_id, now)
        ]
        active_rows = shitposter_memory_dal.active_lore(persona_id, session=session)
        attributes = [
            {"id": a.id, "text": a.text}
            for a in shitposter_memory_dal.active_attributes(persona_id, session=session)
        ]
        retired = guardrails.operator_retired_attributes(
            session, persona_id
        ) + guardrails.operator_retired_lore(session, persona_id)
        # read before any agent call so a failed run still records it
        gate = read_novel_gate(session, persona_id, now)
        refreshes = [r.to_payload() for r in lore_refresh.compute_refreshes(session, persona_id)]
        member_names = guardrails.member_identifiers(session)
    stepped = source_weights.effective_weights(weight_decisions)
    weights = TargetWeights(
        theme=stepped.get(THEME, seeded.theme), novel=stepped.get(NOVEL, seeded.novel)
    )

    # the session is closed before any agent call so no transaction spans it
    ctx = {
        "persona_id": persona_id,
        "active": [{"id": r.id, "text": r.text, "kind": r.kind} for r in active_rows],
        "active_ids": {r.id for r in active_rows},
        "attributes": attributes,
        "retired": retired,
        "member_names": member_names,
        "shares": pool_shares([r.kind for r in active_rows]),
        "weights": weights,
        "gate": gate,
        "cap": cap,
    }
    readings: dict[str, dict[str, Any]] = {
        THEME: _branch_reading(ctx, THEME),
        NOVEL: {**_branch_reading(ctx, NOVEL), "gate": gate.as_details()},
    }
    accepted: list[dict[str, Any]] = []
    rejections: list[dict[str, Any]] = []
    try:
        for kind, branch in _BRANCHES:
            # each kind is asked only for the slots earlier kinds left under the cap
            result = branch(ctx, cap - len(accepted))
            readings[kind] = result["reading"]
            accepted += result["accepted"]
            rejections += result["rejections"]
    except Exception as e:
        # the apply ends the run as failed, keeping the readings taken so far
        logger.error("idea_generate failed persona=%s: %s", persona_id, e)
        return {
            "persona_id": persona_id,
            THEME: readings[THEME],
            NOVEL: readings[NOVEL],
            "proposals": [],
            "rejections": rejections,
            "weight_decisions": weight_decisions,
            "error": f"{type(e).__name__}: {e}",
        }
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
        "refreshes": refreshes,
        "weight_decisions": weight_decisions,
    }


def _cause_ref(cited: list[int]) -> str:
    return ",".join(f"lore:{i}" for i in cited)


def _novel_cause_ref(gate: dict[str, Any]) -> str:
    return NovelGateReading(
        recent_mean=gate["recent_mean"],
        baseline_mean=gate["baseline_mean"],
        fraction=gate["fraction"],
    ).to_cause_ref()


def _apply(session: Session, run_id: int, payload: dict[str, Any]) -> ApplyOutcome:
    persona_id = payload["persona_id"]
    if payload.get("error"):
        # a failed run writes no weight revisions
        details = {
            THEME: payload[THEME],
            NOVEL: payload[NOVEL],
            "entries": [],
            "rejections": list(payload["rejections"]),
            "refreshes": [],
            "weight_changes": [],
        }
        return ApplyOutcome(
            status=ShitposterBrainJobStatus.FAILED.value,
            details=details,
            error=payload["error"],
        )
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
        if p["kind"] == NOVEL:
            cause_kind = ShitposterMemoryCauseKindEnum.NOVEL_GATE.value
            cause_ref = _novel_cause_ref(payload[NOVEL]["gate"])
        else:
            cause_kind = ShitposterMemoryCauseKindEnum.THEME_SYNTHESIS.value
            cause_ref = _cause_ref(cited)
        row = shitposter_memory_dal._add_lore(
            session,
            persona_id=persona_id,
            text=p["text"],
            kind=p["kind"],
            cause_kind=cause_kind,
            source_post_id=None,
            cause_post_id=None,
            cause_ref=cause_ref,
            reflector_run_id=None,
            popularity_score=popularity,
            now=now,
        )
        entries.append(
            {"lore_id": row.id, "kind": p["kind"], "agent_id": p["agent_id"], "cited": cited}
        )
    session.flush()
    # after the entries, so a refresh never retires an id a proposal cited
    refreshes = lore_refresh.apply_refreshes(
        session,
        [lore_refresh.LoreRefresh.from_payload(r) for r in payload.get("refreshes") or []],
        now,
    )
    session.flush()
    weight_changes = source_weights.apply_weight_changes(
        session, persona_id, run_id, payload.get("weight_decisions") or []
    )

    details = {
        THEME: payload[THEME],
        NOVEL: payload[NOVEL],
        "entries": entries,
        "rejections": rejections,
        "refreshes": refreshes,
        "weight_changes": weight_changes,
    }
    stepped = any("new" in w for w in weight_changes)
    status = (
        ShitposterBrainJobStatus.SUCCEEDED
        if entries or refreshes or stepped
        else ShitposterBrainJobStatus.NO_OP
    )
    logger.info(
        "idea_generate applied run=%s status=%s entries=%d rejected=%d refreshed=%d",
        run_id,
        status.value,
        len(entries),
        len(rejections),
        len(refreshes),
    )
    return ApplyOutcome(status=status.value, details=details)


async def register_idea_generate_schedule(
    client: Client,
    task_queue: str,
    persona_id: int,
    app_env: str | None = None,
) -> None:
    """Create the idea-generator schedule for one persona (every FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS).

    When the cadence is "off", any existing schedule is removed and none is created.
    """
    hours = idea_generator_cadence_hours()
    if hours is None:
        await remove_brain_schedule(
            client,
            persona_id,
            ShitposterBrainJobKind.IDEA_GENERATE.value,
            app_env=app_env,
        )
        return
    await register_brain_schedule(
        client,
        task_queue,
        persona_id,
        ShitposterBrainJobKind.IDEA_GENERATE.value,
        every=datetime.timedelta(hours=hours),
        app_env=app_env,
    )


register_job_body(
    ShitposterBrainJobKind.IDEA_GENERATE,
    JobBody(compute=_compute, apply=_apply),
)
