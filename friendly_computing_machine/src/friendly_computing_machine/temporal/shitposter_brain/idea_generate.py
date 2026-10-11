"""Idea-generator brain job: keep generated lore near its standing-pool target.

Compute reads the persona's active lore, compares each generated kind's share
against its target weight, and asks that kind's agent for proposals only when
the share is below target. Each proposal is validated (citations, guardrails)
and the run cap applies across kinds. Apply writes the accepted entries and
their change-log rows in the runner's single transaction. Hit promotion is not
done here; lore_promotion owns it.
"""

import logging
from dataclasses import dataclass
from typing import Any

from sqlmodel import Session

from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    novel_target_weight,
    theme_target_weight,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
    BrainJobInput,
    JobBody,
    register_job_body,
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


@dataclass(frozen=True)
class TargetWeights:
    theme: float
    novel: float


def current_weights(session: Session, persona_id: int) -> TargetWeights:
    """Target share per generated kind; the single seam stored weights replace."""
    return TargetWeights(theme=theme_target_weight(), novel=novel_target_weight())


def pool_shares(active_kinds: list[str]) -> dict[str, float | None]:
    """Share of active lore per generated kind; None for an empty pool (counts as below target)."""
    raise NotImplementedError


def below_target(share: float | None, target: float) -> bool:
    raise NotImplementedError


def median_hit_popularity(session: Session, persona_id: int) -> int:
    """Median popularity of active hit lore, or 0 when there is none."""
    raise NotImplementedError


def _call_theme_agent(
    active: list[dict[str, Any]], retired: list[dict[str, Any]]
) -> str:
    raise NotImplementedError


def _parse_entries(text: str) -> list[Any]:
    raise NotImplementedError


def _validate_theme(
    raw_entries: list[Any],
    active_ids: set[int],
    member_names: list[str],
    retired: list[dict[str, Any]],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Accepted theme entries and rejections ({kind, text, reason})."""
    raise NotImplementedError


def _theme_branch(ctx: dict[str, Any]) -> dict[str, Any]:
    raise NotImplementedError


def _novel_branch(ctx: dict[str, Any]) -> dict[str, Any]:
    """Novel exploration: gate not implemented yet, so it records the reading and emits nothing."""
    raise NotImplementedError


def _enforce_run_cap(
    accepted: list[dict[str, Any]], rejections: list[dict[str, Any]], cap: int
) -> list[dict[str, Any]]:
    raise NotImplementedError


def _compute(params: BrainJobInput) -> dict[str, Any]:
    raise NotImplementedError


def _apply(session: Session, run_id: int, payload: dict[str, Any]) -> ApplyOutcome:
    raise NotImplementedError


register_job_body(
    ShitposterBrainJobKind.IDEA_GENERATE,
    JobBody(compute=_compute, apply=_apply),
)
