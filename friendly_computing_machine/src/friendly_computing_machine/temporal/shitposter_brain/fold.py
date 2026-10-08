"""Fold retired attributes and stale lore into consolidated lore entries.

Two entry points feed the memory DAL's fold_items:
- fold_reflector_retirements: registered as a reflect fold hook; runs inside the
  reflector apply transaction for attributes the reflector retired or merged.
- fold_stale_lore: run by the snapshot job before building a snapshot; folds lore
  whose last snapshot appearance (or creation, if never shown) is at least the
  stale period old.

Rows the operator retired (retired_by_operator) are never folded.
"""

import datetime
import logging
import os

from sqlalchemy import func
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterLoreKindEnum,
    ShitposterMemoryEntityKindEnum,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.reflect import (
    register_fold_hook,
)

logger = logging.getLogger(__name__)

DEFAULT_STALE_LORE_DAYS = 30
STALE_LORE_DAYS_ENV = "FCM_SHITPOSTER_STALE_LORE_DAYS"
MAX_CONSOLIDATED_CHARS = 500
CONSOLIDATED_SEPARATOR = "; "


def load_stale_lore_days() -> int:
    """Read FCM_SHITPOSTER_STALE_LORE_DAYS (default 30; positive integer)."""
    raw = os.environ.get(STALE_LORE_DAYS_ENV, "").strip()
    if not raw:
        return DEFAULT_STALE_LORE_DAYS
    try:
        days = int(raw)
    except ValueError:
        days = 0
    if days < 1:
        logger.warning(
            "%s=%r is not a positive integer; using %d",
            STALE_LORE_DAYS_ENV,
            raw,
            DEFAULT_STALE_LORE_DAYS,
        )
        return DEFAULT_STALE_LORE_DAYS
    return days


def _now() -> datetime.datetime:
    return datetime.datetime.now(datetime.timezone.utc)


def _aware(value: datetime.datetime) -> datetime.datetime:
    # sqlite returns naive datetimes; stored timestamps are UTC
    if value.tzinfo is None:
        return value.replace(tzinfo=datetime.timezone.utc)
    return value


def _consolidated_text(existing: str | None, new_texts: list[str]) -> str:
    """Existing consolidated text plus the new source texts, oldest dropped past the cap."""
    parts = ([existing] if existing else []) + new_texts
    text = CONSOLIDATED_SEPARATOR.join(parts)
    if len(text) > MAX_CONSOLIDATED_CHARS:
        text = text[-MAX_CONSOLIDATED_CHARS:]
        _, sep, tail = text.partition(CONSOLIDATED_SEPARATOR)
        if sep:
            text = tail
    return text


def fold_reflector_retirements(
    session: Session,
    persona_id: int,
    retired_attribute_ids: list[int],
    reflector_run_id: int,
) -> None:
    """Fold reflector-retired or merged attributes into a consolidated lore entry."""
    if not retired_attribute_ids:
        return
    rows = session.exec(
        select(ShitposterPersonaAttribute).where(
            ShitposterPersonaAttribute.id.in_(retired_attribute_ids),  # type: ignore[union-attr]
            ShitposterPersonaAttribute.persona_id == persona_id,
        )
    ).all()
    # Guard by attribute key: a closed predecessor id must not fold when its key was operator-retired.
    keys = {row.attribute_key for row in rows}
    operator_keys = set(
        session.exec(
            select(ShitposterPersonaAttribute.attribute_key).where(
                ShitposterPersonaAttribute.persona_id == persona_id,
                ShitposterPersonaAttribute.attribute_key.in_(keys),  # type: ignore[union-attr]
                ShitposterPersonaAttribute.retired_by_operator.is_(True),  # type: ignore[attr-defined]
            )
        ).all()
    )
    foldable = [
        row
        for row in rows
        if not row.retired_by_operator and row.attribute_key not in operator_keys
    ]
    if not foldable:
        return
    existing = shitposter_memory_dal.current_consolidated_lore(persona_id, session=session)
    shitposter_memory_dal.fold_items(
        session,
        lore_ids=[existing.id] if existing else [],
        attribute_ids=[row.id for row in foldable],
        into_text=_consolidated_text(
            existing.text if existing else None, [row.text for row in foldable]
        ),
        cause_ref=f"reflector-run:{reflector_run_id}",
        reflector_run_id=str(reflector_run_id),
        now=_now(),
    )
    logger.info(
        "fold: persona=%s folded %d reflector-retired attribute(s) from run=%s",
        persona_id,
        len(foldable),
        reflector_run_id,
    )


def fold_stale_lore(
    persona_id: int,
    now: datetime.datetime | None = None,
    stale_days: int | None = None,
    session: Session | None = None,
) -> int:
    """Fold lore past the stale period; returns the number of source rows folded.

    Pass the snapshot job's session to fold inside its transaction; without one,
    the fold commits on its own session.
    """
    now = _aware(now) if now is not None else _now()
    days = stale_days if stale_days is not None else load_stale_lore_days()
    cutoff = now - datetime.timedelta(days=days)
    with SessionManager(session) as s:
        hits = list(
            s.exec(
                select(ShitposterLoreEntry)
                .where(ShitposterLoreEntry.persona_id == persona_id)
                .where(ShitposterLoreEntry.valid_to.is_(None))  # type: ignore[union-attr]
                .where(ShitposterLoreEntry.kind == ShitposterLoreKindEnum.HIT.value)
                .where(ShitposterLoreEntry.retired_by_operator.is_(False))  # type: ignore[attr-defined]
            ).all()
        )
        if not hits:
            return 0
        last_seen: dict[int, datetime.datetime] = {
            item_id: _aware(ts)
            for item_id, ts in s.exec(
                select(
                    ShitposterSnapshotItem.item_id,
                    func.max(ShitposterContextSnapshot.created_at),
                )
                .join(
                    ShitposterContextSnapshot,
                    ShitposterSnapshotItem.snapshot_id == ShitposterContextSnapshot.id,  # type: ignore[arg-type]
                )
                .where(ShitposterContextSnapshot.persona_id == persona_id)
                .where(
                    ShitposterSnapshotItem.item_kind == ShitposterMemoryEntityKindEnum.LORE.value
                )
                .where(ShitposterSnapshotItem.item_id.in_([h.id for h in hits]))  # type: ignore[union-attr]
                .group_by(ShitposterSnapshotItem.item_id)
            ).all()
        }
        stale = [
            h
            for h in hits
            if last_seen.get(h.id, _aware(h.valid_from)) <= cutoff
        ]
        if not stale:
            return 0
        existing = shitposter_memory_dal.current_consolidated_lore(persona_id, session=s)
        shitposter_memory_dal.fold_items(
            s,
            lore_ids=[h.id for h in stale] + ([existing.id] if existing else []),
            attribute_ids=[],
            into_text=_consolidated_text(
                existing.text if existing else None, [h.text for h in stale]
            ),
            cause_ref=f"stale-lore:{now.date().isoformat()}",
            reflector_run_id=None,
            now=now,
        )
        if session is None:
            s.commit()
    logger.info(
        "fold: persona=%s folded %d stale lore entries past %d day(s)",
        persona_id,
        len(stale),
        days,
    )
    return len(stale)


register_fold_hook(fold_reflector_retirements)
