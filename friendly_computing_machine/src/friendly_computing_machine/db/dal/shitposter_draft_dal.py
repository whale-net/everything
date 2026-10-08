"""Shitposter draft queue reads and discards. Writes that create drafts happen in the write brain job.

Drafts are picked best-rank first. Expiry is checked in Python on created_at so
timezone handling matches the rest of the app across SQLite and Postgres.
"""

import datetime
from typing import Optional

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_draft import (
    ShitposterDraft,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterAttributeStatusEnum,
    ShitposterLoreEntry,
    ShitposterPersonaAttribute,
)

ITEM_ATTRIBUTE = "attribute"
ITEM_LORE = "lore"


def aware(value: datetime.datetime) -> datetime.datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=datetime.timezone.utc)
    return value


def unused_drafts(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterDraft]:
    """Unused, undiscarded drafts for a persona, best rank first (oldest first on ties)."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterDraft)
                .where(ShitposterDraft.persona_id == persona_id)
                .where(ShitposterDraft.used_at.is_(None))  # type: ignore[union-attr]
                .where(ShitposterDraft.discarded_at.is_(None))  # type: ignore[union-attr]
                .order_by(ShitposterDraft.rank, ShitposterDraft.created_at, ShitposterDraft.id)  # type: ignore[arg-type]
            ).all()
        )


def is_unexpired(
    draft: ShitposterDraft, now: datetime.datetime, expiry: datetime.timedelta
) -> bool:
    return now - aware(draft.created_at) <= expiry


def discard_draft(
    session: Session, draft: ShitposterDraft, reason: str, now: datetime.datetime
) -> None:
    """Mark a draft discarded. Caller commits."""
    draft.discarded_at = now
    draft.discard_reason = reason
    session.add(draft)


def snapshot_has_retired_item(session: Session, snapshot_id: int) -> bool:
    """True when the snapshot includes an attribute or lore entry the operator has since retired."""
    items = session.exec(
        select(ShitposterSnapshotItem).where(
            ShitposterSnapshotItem.snapshot_id == snapshot_id
        )
    ).all()
    for item in items:
        if item.item_kind == ITEM_ATTRIBUTE:
            if _attribute_retired(session, item.item_id):
                return True
        elif item.item_kind == ITEM_LORE:
            row = session.get(ShitposterLoreEntry, item.item_id)
            if row is not None and row.retired_by_operator:
                return True
    return False


def _attribute_retired(session: Session, row_id: int) -> bool:
    row = session.get(ShitposterPersonaAttribute, row_id)
    if row is None:
        return False
    if row.retired_by_operator:
        return True
    # a retirement lands as a new current row with the same key
    current = session.exec(
        select(ShitposterPersonaAttribute)
        .where(ShitposterPersonaAttribute.persona_id == row.persona_id)
        .where(ShitposterPersonaAttribute.attribute_key == row.attribute_key)
        .where(ShitposterPersonaAttribute.valid_to.is_(None))  # type: ignore[union-attr]
    ).first()
    return (
        current is not None
        and current.status == ShitposterAttributeStatusEnum.RETIRED.value
        and current.retired_by_operator
    )
