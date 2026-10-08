"""Read API for Shitposter context snapshots. Writes happen in the snapshot brain job."""

from typing import Optional

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)


def latest_snapshot(
    persona_id: int, session: Optional[Session] = None
) -> Optional[ShitposterContextSnapshot]:
    """Highest-version snapshot for a persona, or None if none has been written."""
    with SessionManager(session) as s:
        return s.exec(
            select(ShitposterContextSnapshot)
            .where(ShitposterContextSnapshot.persona_id == persona_id)
            .order_by(ShitposterContextSnapshot.version.desc())  # type: ignore[union-attr]
            .limit(1)
        ).first()


def snapshot_by_id(
    snapshot_id: int, session: Optional[Session] = None
) -> Optional[ShitposterContextSnapshot]:
    with SessionManager(session) as s:
        return s.get(ShitposterContextSnapshot, snapshot_id)


def snapshot_items(
    snapshot_id: int, session: Optional[Session] = None
) -> list[ShitposterSnapshotItem]:
    """Items a snapshot ranked in, in rank order."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterSnapshotItem)
                .where(ShitposterSnapshotItem.snapshot_id == snapshot_id)
                .order_by(ShitposterSnapshotItem.rank)
            ).all()
        )
