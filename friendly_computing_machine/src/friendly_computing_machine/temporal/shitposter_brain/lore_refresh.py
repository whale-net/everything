"""Earned popularity refresh for generated (theme/novel) lore.

Each idea-generator run recomputes an active generated entry's popularity as
the mean popularity of finalized posts written from snapshots that held any
revision of the entry (matched by lore_key). Compute reads only; apply opens a
new SCD2 revision under the same lore_key and logs a refresh. An entry with no
such posts, or whose popularity already equals the mean, gets no revision.
"""

import dataclasses
import datetime
import logging
import statistics
import uuid
from dataclasses import dataclass
from typing import Any

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    is_generated_lore,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.lore_promotion import (
    popularity_score,
)

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class LoreRefresh:
    """One planned refresh: the entry's current row, its old and earned popularity, and the posts behind it."""

    lore_id: int
    lore_key: uuid.UUID
    old: int
    new: int
    post_ids: tuple[int, ...]

    def as_details(self) -> dict[str, Any]:
        """Run-details row: refreshes: [{lore_id, lore_key, old, new, post_count}]."""
        return {
            "lore_id": self.lore_id,
            "lore_key": str(self.lore_key),
            "old": self.old,
            "new": self.new,
            "post_count": len(self.post_ids),
        }

    def to_payload(self) -> dict[str, Any]:
        """JSON-safe form carried from compute to apply."""
        return {
            "lore_id": self.lore_id,
            "lore_key": str(self.lore_key),
            "old": self.old,
            "new": self.new,
            "post_ids": list(self.post_ids),
        }

    @classmethod
    def from_payload(cls, raw: dict[str, Any]) -> "LoreRefresh":
        return cls(
            lore_id=int(raw["lore_id"]),
            lore_key=uuid.UUID(str(raw["lore_key"])),
            old=int(raw["old"]),
            new=int(raw["new"]),
            post_ids=tuple(int(i) for i in raw["post_ids"]),
        )


def _contributing_posts(
    session: Session, lore_key: uuid.UUID
) -> list[tuple[int, int]]:
    """(post_id, popularity) for each distinct finalized post written from a snapshot holding lore_key."""
    snapshot_ids = {
        item.snapshot_id
        for item in shitposter_memory_dal.snapshot_items_for_key(lore_key, session=session)
    }
    if not snapshot_ids:
        return []
    rows = session.exec(
        select(
            ShitposterPost.id,
            ShitposterPostEngagement.distinct_reactors,
            ShitposterPostEngagement.distinct_repliers,
        )
        .join(
            ShitposterPostEngagement,
            ShitposterPostEngagement.post_id == ShitposterPost.id,  # type: ignore[arg-type]
        )
        .where(ShitposterPost.context_snapshot_id.in_(snapshot_ids))  # type: ignore[union-attr]
        .order_by(ShitposterPost.id)  # type: ignore[arg-type]
    ).all()
    seen: dict[int, int] = {}
    for post_id, reactors, repliers in rows:
        seen.setdefault(post_id, popularity_score(reactors, repliers))
    return list(seen.items())


def contributing_post_ids(session: Session, lore_key: uuid.UUID) -> list[int]:
    """Distinct ids of finalized posts written from snapshots holding any revision of lore_key."""
    return [post_id for post_id, _ in _contributing_posts(session, lore_key)]


def compute_refreshes(session: Session, persona_id: int) -> list[LoreRefresh]:
    """Planned refreshes for the persona's active theme/novel entries; writes nothing."""
    refreshes: list[LoreRefresh] = []
    for row in shitposter_memory_dal.active_lore(persona_id, session=session):
        if not is_generated_lore(row.kind):
            continue
        posts = _contributing_posts(session, row.lore_key)
        if not posts:
            continue
        earned = round(statistics.fmean(score for _, score in posts))
        if earned == row.popularity_score:
            continue
        refreshes.append(
            LoreRefresh(
                lore_id=row.id,
                lore_key=row.lore_key,
                old=row.popularity_score,
                new=earned,
                post_ids=tuple(post_id for post_id, _ in posts),
            )
        )
    return refreshes


def apply_refreshes(
    session: Session, refreshes: list[LoreRefresh], now: datetime.datetime
) -> list[dict[str, Any]]:
    """Write each planned refresh still matching the current row; returns run-details rows."""
    details: list[dict[str, Any]] = []
    for planned in refreshes:
        current = session.get(ShitposterLoreEntry, planned.lore_id)
        # retired, folded, or already revised since compute
        if (
            current is None
            or current.valid_to is not None
            or current.popularity_score != planned.old
        ):
            logger.info(
                "lore refresh skipped: lore=%s no longer matches its planned row",
                planned.lore_id,
            )
            continue
        successor, _ = shitposter_memory_dal.refresh_lore_popularity(
            session,
            planned.lore_id,
            popularity_score=planned.new,
            post_ids=list(planned.post_ids),
            now=now,
        )
        details.append(dataclasses.replace(planned, lore_id=successor.id).as_details())
    return details
