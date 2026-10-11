"""Adaptive theme/novel target weights, stepped once per idea-generator run.

Compute compares, per source, the mean popularity of finalized posts whose
context snapshot held that source's lore against posts without it, over days
in a trailing window on which both kinds were written. It proposes one step up
or down, clamps to the source's bounds, and drops any step that would breach
the combined ceiling. Compute is write-free; apply writes the SCD2 revisions in
the run's transaction, so a failed run writes none.
"""

import datetime
import logging
from dataclasses import dataclass
from typing import Any

from sqlmodel import Session

from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    SourceWeightConfig,
)

logger = logging.getLogger(__name__)

# Reasons a source's weight stays unchanged on a run.
REASON_BELOW_MINIMUM = "below_minimum"
REASON_EQUAL = "equal_means"
REASON_BOUND = "bound"
REASON_CEILING = "ceiling"


@dataclass(frozen=True)
class SourceComparison:
    """One source's with/without popularity reading over the overlapping days."""

    source: str
    with_mean: float | None
    without_mean: float | None
    with_n: int
    without_n: int
    days_compared: int

    def as_cause(self) -> dict[str, Any]:
        return {
            "with_mean": self.with_mean,
            "without_mean": self.without_mean,
            "with_n": self.with_n,
            "without_n": self.without_n,
            "days_compared": self.days_compared,
        }


@dataclass(frozen=True)
class WeightDecision:
    """A source's outcome for one run; `new` is None when `unchanged_reason` is set."""

    source: str
    old: float
    new: float | None
    comparison: SourceComparison
    unchanged_reason: str | None = None

    def as_details(self) -> dict[str, Any]:
        raise NotImplementedError


def compare_source(
    session: Session,
    persona_id: int,
    source: str,
    now: datetime.datetime,
    window_days: int,
) -> SourceComparison:
    """With/without mean popularity for `source` over days in the window with both kinds of post."""
    raise NotImplementedError


def resolve_steps(
    comparisons: dict[str, SourceComparison],
    current: dict[str, float],
    config: SourceWeightConfig,
    step: float,
    min_posts: int,
) -> list[WeightDecision]:
    """Per-source decisions after the minimum, bounds, ceiling and tie-break rules."""
    raise NotImplementedError


def compute_weight_changes(
    session: Session, persona_id: int, now: datetime.datetime
) -> list[WeightDecision]:
    """This run's weight decisions; reads only."""
    raise NotImplementedError


def apply_weight_changes(
    session: Session,
    persona_id: int,
    run_id: int,
    decisions: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    """Write a revision for each changed source; returns the run's weight_changes details."""
    raise NotImplementedError
