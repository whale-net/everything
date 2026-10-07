"""Shitposter memory DAL: SCD2 persona attributes and lore, each write paired
with an append-only change-log row in the same transaction.

Every write takes a cause; a call without one raises.
"""

import datetime
from typing import Optional

from sqlmodel import Session

from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
)


def add_attribute(
    persona_id: int,
    attribute_key: str,
    text: str,
    cause_kind: str,
    cause_post_id: Optional[int] = None,
    cause_ref: Optional[str] = None,
    reflector_run_id: Optional[str] = None,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterPersonaAttribute:
    """Open a new current attribute and log an add. Requires a cause."""
    raise NotImplementedError


def reinforce_attribute(
    attribute_id: int,
    cause_kind: str,
    cause_post_id: Optional[int] = None,
    cause_ref: Optional[str] = None,
    reflector_run_id: Optional[str] = None,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterPersonaAttribute:
    """Close and reopen the current attribute row, logging a reinforce."""
    raise NotImplementedError


def retire_attribute(
    attribute_id: int,
    cause_kind: str,
    cause_post_id: Optional[int] = None,
    cause_ref: Optional[str] = None,
    reflector_run_id: Optional[str] = None,
    retired_by_operator: bool = False,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterPersonaAttribute:
    """Close the current attribute row with status retired, logging a retire."""
    raise NotImplementedError


def merge_attributes(
    from_attribute_id: int,
    into_attribute_id: int,
    cause_kind: str,
    cause_post_id: Optional[int] = None,
    cause_ref: Optional[str] = None,
    reflector_run_id: Optional[str] = None,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterPersonaAttribute:
    """Retire the source attribute as merged into the target, logging a merge."""
    raise NotImplementedError


def add_lore(
    persona_id: int,
    text: str,
    kind: str,
    cause_kind: str,
    source_post_id: Optional[int] = None,
    cause_post_id: Optional[int] = None,
    cause_ref: Optional[str] = None,
    reflector_run_id: Optional[str] = None,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterLoreEntry:
    """Open a new current lore entry and log an add. Requires a cause."""
    raise NotImplementedError


def retire_lore(
    lore_id: int,
    cause_kind: str,
    cause_post_id: Optional[int] = None,
    cause_ref: Optional[str] = None,
    reflector_run_id: Optional[str] = None,
    retired_by_operator: bool = False,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterLoreEntry:
    """Close the current lore row as retired, logging a retire."""
    raise NotImplementedError


def fold_lore(
    from_lore_ids: list[int],
    into_text: str,
    cause_ref: str,
    reflector_run_id: Optional[str] = None,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterLoreEntry:
    """Retire several lore rows into one consolidated entry, logging folds."""
    raise NotImplementedError


def operator_retire(
    entity_kind: str,
    entity_id: int,
    operator_subject: str,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterMemoryChange:
    """Operator retire of an attribute or lore row; cause_kind is operator."""
    raise NotImplementedError


def active_attributes(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterPersonaAttribute]:
    """Current attributes for a persona."""
    raise NotImplementedError


def active_lore(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterLoreEntry]:
    """Current lore entries for a persona."""
    raise NotImplementedError


def change_history(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterMemoryChange]:
    """Change log for a persona, newest first."""
    raise NotImplementedError


def attributes_as_of(
    persona_id: int, as_of: datetime.datetime, session: Optional[Session] = None
) -> list[ShitposterPersonaAttribute]:
    """Point-in-time attribute rows for a persona."""
    raise NotImplementedError
