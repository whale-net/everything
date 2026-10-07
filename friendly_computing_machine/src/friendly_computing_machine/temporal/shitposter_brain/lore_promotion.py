"""Promote popular bot posts into lore entries.

Runs as a post-step of the harvest apply, in the harvest run's transaction. For
each newly finalized engagement row (consumed_by_lore_run_id NULL), a post whose
popularity ranks in the top share of finalized posts from the last window is
inserted as a `hit` lore entry via the memory DAL (cause_kind=post_engagement),
then the engagement row is marked consumed by this run.

Never promoted: a post with more negative than other reactions, a post that
targets a named member, or a post whose earlier lore entry the operator retired.
Lore is not removed when the post leaves the window.
"""

import datetime
from dataclasses import dataclass

from sqlmodel import Session

from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
)

DEFAULT_TOP_SHARE = 0.10
DEFAULT_WINDOW_DAYS = 30


@dataclass(frozen=True)
class PromotionCandidate:
    post_id: int
    persona_id: int
    popularity_score: int
    negative_dominant: bool
    targets_named_member: bool


def popularity_score(distinct_reactors: int, distinct_repliers: int) -> int:
    """Lifetime popularity of a finalized post: reactors plus repliers."""
    raise NotImplementedError


def cutoff_rank_count(finalized_in_window: int, top_share: float) -> int:
    """How many of the window's finalized posts fall in the top share (at least 1 if any)."""
    raise NotImplementedError


def promote_new_lore(
    session: Session,
    run_id: int,
    persona_id: int,
    now: datetime.datetime,
    top_share: float = DEFAULT_TOP_SHARE,
    window_days: int = DEFAULT_WINDOW_DAYS,
) -> ApplyOutcome:
    """Insert lore for newly finalized popular posts and mark their engagement consumed."""
    raise NotImplementedError
