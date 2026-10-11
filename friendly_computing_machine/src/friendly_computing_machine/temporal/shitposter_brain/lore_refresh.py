"""Earned popularity refresh for generated (theme/novel) lore.

Each idea-generator run recomputes an active generated entry's popularity as
the mean popularity of finalized posts written from snapshots that held any
revision of the entry (matched by lore_key). Compute reads only; apply opens a
new SCD2 revision under the same lore_key and logs a refresh. An entry with no
such posts keeps its popularity and gets no revision.
"""

import datetime
import uuid
from dataclasses import dataclass
from typing import Any

from sqlmodel import Session


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
        raise NotImplementedError


def contributing_post_ids(session: Session, lore_key: uuid.UUID) -> list[int]:
    """Distinct ids of finalized posts written from snapshots holding any revision of lore_key."""
    raise NotImplementedError


def compute_refreshes(session: Session, persona_id: int) -> list[LoreRefresh]:
    """Planned refreshes for the persona's active theme/novel entries; writes nothing."""
    raise NotImplementedError


def apply_refreshes(
    session: Session, refreshes: list[LoreRefresh], now: datetime.datetime
) -> list[dict[str, Any]]:
    """Write each planned refresh still matching the current row; returns run-details rows."""
    raise NotImplementedError
