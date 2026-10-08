"""Fold retired attributes and stale lore into consolidated lore entries.

Two entry points feed the memory DAL's fold_lore:
- fold_reflector_retirements: registered as a reflect fold hook; runs inside the
  reflector apply transaction for attributes the reflector retired or merged.
- fold_stale_lore: run by the snapshot job before building a snapshot; folds lore
  whose last snapshot appearance (or creation, if never shown) is older than the
  stale period.

Rows the operator retired (retired_by_operator) are never folded.
"""

import datetime

from sqlmodel import Session

DEFAULT_STALE_LORE_DAYS = 30
STALE_LORE_DAYS_ENV = "FCM_SHITPOSTER_STALE_LORE_DAYS"


def load_stale_lore_days() -> int:
    """Read FCM_SHITPOSTER_STALE_LORE_DAYS (default 30; positive integer)."""
    raise NotImplementedError


def fold_reflector_retirements(
    session: Session,
    persona_id: int,
    retired_attribute_ids: list[int],
    reflector_run_id: int,
) -> None:
    """Fold reflector-retired or merged attributes into a consolidated lore entry."""
    raise NotImplementedError


def fold_stale_lore(
    persona_id: int,
    now: datetime.datetime | None = None,
    stale_days: int | None = None,
) -> int:
    """Fold lore past the stale period; returns the number of source rows folded."""
    raise NotImplementedError


