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
import statistics
from dataclasses import dataclass
from typing import Any

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_weight_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterMemoryEntityKindEnum,
    ShitposterSourceWeight,
    ShitposterWeightSourceEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    SourceWeightConfig,
    load_source_weight_config,
    weight_min_posts,
    weight_step,
    weight_window_days,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.lore_promotion import (
    popularity_score,
)

logger = logging.getLogger(__name__)

THEME = ShitposterWeightSourceEnum.THEME.value
NOVEL = ShitposterWeightSourceEnum.NOVEL.value
SOURCES = (THEME, NOVEL)

# Reasons a source's weight stays unchanged on a run.
REASON_BELOW_MINIMUM = "below_minimum"
REASON_EQUAL = "equal_means"
REASON_BOUND = "bound"
REASON_CEILING = "ceiling"
# the stored weight moved between compute and apply
REASON_SUPERSEDED = "superseded"

_EPS = 1e-9
# matches the Numeric(6, 4) weight column
_PRECISION = 4


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

    @property
    def changed(self) -> bool:
        return self.unchanged_reason is None and self.new is not None

    def as_details(self) -> dict[str, Any]:
        """The run's weight_changes row for this source."""
        if not self.changed:
            return {"source": self.source, "unchanged_reason": self.unchanged_reason}
        return {
            "source": self.source,
            "old": self.old,
            "new": self.new,
            "with_mean": self.comparison.with_mean,
            "without_mean": self.comparison.without_mean,
        }

    def as_payload(self) -> dict[str, Any]:
        """JSON form carried from compute to apply."""
        return {
            "source": self.source,
            "old": self.old,
            "new": self.new,
            "unchanged_reason": self.unchanged_reason,
            "cause": self.comparison.as_cause(),
        }


def _utc(dt: datetime.datetime) -> datetime.datetime:
    # sqlite returns naive datetimes; stored timestamps are UTC
    return dt if dt.tzinfo is not None else dt.replace(tzinfo=datetime.timezone.utc)


def _snapshots_with_source(
    session: Session, snapshot_ids: set[int], source: str
) -> set[int]:
    """Snapshots among `snapshot_ids` that included a lore row version of kind `source`."""
    if not snapshot_ids:
        return set()
    rows = session.exec(
        select(ShitposterSnapshotItem.snapshot_id)
        .join(ShitposterLoreEntry, ShitposterLoreEntry.id == ShitposterSnapshotItem.item_id)
        .where(
            ShitposterSnapshotItem.snapshot_id.in_(snapshot_ids),  # type: ignore[attr-defined]
            ShitposterSnapshotItem.item_kind == ShitposterMemoryEntityKindEnum.LORE.value,
            ShitposterLoreEntry.kind == source,
        )
    ).all()
    return set(rows)


def _finalized_posts(
    session: Session, persona_id: int, since: datetime.datetime, until: datetime.datetime
) -> list[tuple[datetime.date, int, int]]:
    """(UTC day written, snapshot id, popularity) of finalized posts written in the window.

    Posts without a recorded snapshot are left out: their context is unknown.
    """
    rows = session.exec(
        select(
            ShitposterPost.created_at,
            ShitposterPost.context_snapshot_id,
            ShitposterPostEngagement.distinct_reactors,
            ShitposterPostEngagement.distinct_repliers,
        )
        .join(ShitposterPostEngagement, ShitposterPostEngagement.post_id == ShitposterPost.id)
        .where(
            ShitposterPost.persona_id == persona_id,
            ShitposterPost.context_snapshot_id.is_not(None),  # type: ignore[union-attr]
        )
    ).all()
    out = []
    for created_at, snapshot_id, reactors, repliers in rows:
        written = _utc(created_at)
        if since <= written <= until:
            out.append((written.date(), snapshot_id, popularity_score(reactors, repliers)))
    return out


def _compare(
    source: str,
    posts: list[tuple[datetime.date, int, int]],
    with_snapshots: set[int],
) -> SourceComparison:
    with_days: dict[datetime.date, list[int]] = {}
    without_days: dict[datetime.date, list[int]] = {}
    for day, snapshot_id, score in posts:
        bucket = with_days if snapshot_id in with_snapshots else without_days
        bucket.setdefault(day, []).append(score)
    days = with_days.keys() & without_days.keys()
    with_scores = [s for d in days for s in with_days[d]]
    without_scores = [s for d in days for s in without_days[d]]
    return SourceComparison(
        source=source,
        with_mean=statistics.fmean(with_scores) if with_scores else None,
        without_mean=statistics.fmean(without_scores) if without_scores else None,
        with_n=len(with_scores),
        without_n=len(without_scores),
        days_compared=len(days),
    )


def compare_source(
    session: Session,
    persona_id: int,
    source: str,
    now: datetime.datetime,
    window_days: int,
) -> SourceComparison:
    """With/without mean popularity for `source` over days in the window with both kinds of post."""
    posts = _finalized_posts(
        session, persona_id, now - datetime.timedelta(days=window_days), now
    )
    with_snapshots = _snapshots_with_source(session, {p[1] for p in posts}, source)
    return _compare(source, posts, with_snapshots)


def _bounds(config: SourceWeightConfig, source: str) -> tuple[float, float]:
    if source == THEME:
        return config.theme_min, config.theme_max
    return config.novel_min, config.novel_max


def _proposal(
    comparison: SourceComparison,
    old: float,
    lo: float,
    hi: float,
    step: float,
    min_posts: int,
) -> tuple[float | None, str | None]:
    """(new weight, None) for a step, or (None, reason) when the source stays put."""
    if comparison.with_n < min_posts or comparison.without_n < min_posts:
        return None, REASON_BELOW_MINIMUM
    assert comparison.with_mean is not None and comparison.without_mean is not None
    if comparison.with_mean > comparison.without_mean:
        direction = 1
    elif comparison.with_mean < comparison.without_mean:
        direction = -1
    else:
        return None, REASON_EQUAL
    new = round(min(max(old + direction * step, lo), hi), _PRECISION)
    if abs(new - old) < _EPS:
        return None, REASON_BOUND
    return new, None


def resolve_steps(
    comparisons: dict[str, SourceComparison],
    current: dict[str, float],
    config: SourceWeightConfig,
    step: float,
    min_posts: int,
) -> list[WeightDecision]:
    """Per-source decisions after the minimum, bounds, ceiling and tie-break rules."""
    proposals: dict[str, tuple[float | None, str | None]] = {}
    for source in SOURCES:
        lo, hi = _bounds(config, source)
        proposals[source] = _proposal(
            comparisons[source], current[source], lo, hi, step, min_posts
        )

    def total(applied: set[str]) -> float:
        return sum(
            proposals[s][0] if s in applied else current[s]  # type: ignore[misc]
            for s in SOURCES
        )

    stepping = {s for s in SOURCES if proposals[s][0] is not None}
    if total(stepping) > config.ceiling + _EPS:
        rising = [s for s in stepping if proposals[s][0] > current[s]]  # type: ignore[operator]
        keep = stepping - set(rising)
        # when both rise and only one fits, the one further below its own upper bound steps
        rising.sort(key=lambda s: (_bounds(config, s)[1] - current[s]), reverse=True)
        for s in rising:
            if total(keep | {s}) <= config.ceiling + _EPS:
                keep.add(s)
                break
        for s in stepping - keep:
            proposals[s] = (None, REASON_CEILING)

    return [
        WeightDecision(
            source=s,
            old=current[s],
            new=proposals[s][0],
            comparison=comparisons[s],
            unchanged_reason=proposals[s][1],
        )
        for s in SOURCES
    ]


def _current_weights(session: Session, persona_id: int) -> dict[str, float]:
    rows = session.exec(
        select(ShitposterSourceWeight).where(
            ShitposterSourceWeight.persona_id == persona_id,
            ShitposterSourceWeight.valid_to.is_(None),  # type: ignore[union-attr]
        )
    ).all()
    return {r.source: float(r.weight) for r in rows}


def compute_weight_changes(
    session: Session, persona_id: int, now: datetime.datetime
) -> list[WeightDecision]:
    """This run's weight decisions; reads only.

    The persona's weights must already be seeded (idea_generate.current_weights does it).
    """
    config = load_source_weight_config()
    current = _current_weights(session, persona_id)
    missing = [s for s in SOURCES if s not in current]
    if missing:
        raise LookupError(f"persona {persona_id} has no current weight for {missing}")
    window = weight_window_days()
    posts = _finalized_posts(session, persona_id, now - datetime.timedelta(days=window), now)
    snapshot_ids = {p[1] for p in posts}
    comparisons = {
        s: _compare(s, posts, _snapshots_with_source(session, snapshot_ids, s))
        for s in SOURCES
    }
    decisions = resolve_steps(comparisons, current, config, weight_step(), weight_min_posts())
    logger.info(
        "source weights computed persona=%s %s",
        persona_id,
        ", ".join(
            f"{d.source} {d.old}->{d.new}" if d.changed else f"{d.source} {d.unchanged_reason}"
            for d in decisions
        ),
    )
    return decisions


def effective_weights(decisions: list[dict[str, Any]]) -> dict[str, float]:
    """Per-source weight this run targets: the new weight when it steps, else the old."""
    return {
        d["source"]: d["new"] if d.get("unchanged_reason") is None and d.get("new") is not None else d["old"]
        for d in decisions
    }


def apply_weight_changes(
    session: Session,
    persona_id: int,
    run_id: int,
    decisions: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    """Write a revision for each changed source; returns the run's weight_changes details."""
    current = _current_weights(session, persona_id)
    details: list[dict[str, Any]] = []
    for d in decisions:
        source = d["source"]
        if d.get("unchanged_reason") is not None or d.get("new") is None:
            details.append({"source": source, "unchanged_reason": d.get("unchanged_reason")})
            continue
        stored = current.get(source)
        if stored is None or abs(stored - float(d["old"])) > _EPS:
            # another writer moved the weight after compute read it
            logger.warning(
                "source weight %s for persona %s moved from %s to %s before apply; not stepping",
                source, persona_id, d["old"], stored,
            )
            details.append({"source": source, "unchanged_reason": REASON_SUPERSEDED})
            continue
        shitposter_weight_dal.revise_weight(
            session, persona_id, source, d["new"], dict(d["cause"]), run_id=run_id
        )
        details.append(
            {
                "source": source,
                "old": d["old"],
                "new": d["new"],
                "with_mean": d["cause"].get("with_mean"),
                "without_mean": d["cause"].get("without_mean"),
            }
        )
    return details
