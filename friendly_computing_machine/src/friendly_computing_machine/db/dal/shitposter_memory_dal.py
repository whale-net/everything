"""Shitposter memory DAL: SCD2 persona attributes and lore, each write paired
with an append-only change-log row in the same transaction.

Every write takes a cause; a call without one raises ValueError. `entity_id`
in the change log is the row id the operation acted on (the row being closed,
or the new row for an add).
"""

import datetime
from typing import Optional

from sqlalchemy import func, or_
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobRun,
    ShitposterPostEngagement,
    ShitposterReflectorRun,
    ShitposterSuggestion,
    ShitposterSuggestionBacker,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterAttributeStatusEnum,
    ShitposterLoreEntry,
    ShitposterLoreKindEnum,
    ShitposterMemoryCauseKindEnum,
    ShitposterMemoryChange,
    ShitposterMemoryEntityKindEnum,
    ShitposterMemoryOperationEnum,
    ShitposterPersonaAttribute,
)

SEED_ATTRIBUTE_KEY = "persona_base"
SEED_CAUSE_REF = "seed:persona-revision"

_CAUSE_KINDS = frozenset(c.value for c in ShitposterMemoryCauseKindEnum)
_LORE_KINDS = frozenset(k.value for k in ShitposterLoreKindEnum)
_ENTITY_KINDS = frozenset(k.value for k in ShitposterMemoryEntityKindEnum)


def _now(now: Optional[datetime.datetime]) -> datetime.datetime:
    return now or datetime.datetime.now(datetime.UTC)


def _require_cause(
    cause_kind: str, cause_post_id: Optional[int], cause_ref: Optional[str]
) -> None:
    if cause_kind not in _CAUSE_KINDS:
        raise ValueError(f"unknown cause_kind {cause_kind!r}")
    if cause_post_id is None and not cause_ref:
        raise ValueError("memory change requires a cause: cause_post_id or cause_ref")


def _log(
    session: Session,
    *,
    persona_id: int,
    entity_kind: str,
    entity_id: Optional[int],
    operation: ShitposterMemoryOperationEnum,
    before_text: Optional[str],
    after_text: Optional[str],
    cause_kind: str,
    cause_post_id: Optional[int],
    cause_ref: Optional[str],
    reflector_run_id: Optional[str],
    now: datetime.datetime,
) -> ShitposterMemoryChange:
    change = ShitposterMemoryChange(
        changed_at=now,
        persona_id=persona_id,
        entity_kind=entity_kind,
        entity_id=entity_id,
        operation=operation.value,
        before_text=before_text,
        after_text=after_text,
        cause_kind=cause_kind,
        cause_post_id=cause_post_id,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
    )
    session.add(change)
    return change


def _current_attribute(session: Session, attribute_id: int) -> ShitposterPersonaAttribute:
    row = session.exec(
        select(ShitposterPersonaAttribute)
        .where(ShitposterPersonaAttribute.id == attribute_id)
        .where(ShitposterPersonaAttribute.valid_to.is_(None))
    ).one_or_none()
    if row is None:
        raise LookupError(f"no current attribute with id {attribute_id}")
    return row


def _current_lore(session: Session, lore_id: int) -> ShitposterLoreEntry:
    row = session.exec(
        select(ShitposterLoreEntry)
        .where(ShitposterLoreEntry.id == lore_id)
        .where(ShitposterLoreEntry.valid_to.is_(None))
    ).one_or_none()
    if row is None:
        raise LookupError(f"no current lore entry with id {lore_id}")
    return row


def _add_attribute(
    session: Session,
    *,
    persona_id: int,
    attribute_key: str,
    text: str,
    cause_kind: str,
    cause_post_id: Optional[int],
    cause_ref: Optional[str],
    reflector_run_id: Optional[str],
    now: datetime.datetime,
) -> ShitposterPersonaAttribute:
    _require_cause(cause_kind, cause_post_id, cause_ref)
    existing = session.exec(
        select(ShitposterPersonaAttribute)
        .where(ShitposterPersonaAttribute.persona_id == persona_id)
        .where(ShitposterPersonaAttribute.attribute_key == attribute_key)
        .where(ShitposterPersonaAttribute.valid_to.is_(None))
    ).one_or_none()
    if existing is not None:
        raise ValueError(f"attribute {attribute_key!r} already has a current row")
    row = ShitposterPersonaAttribute(
        persona_id=persona_id,
        attribute_key=attribute_key,
        text=text,
        status=ShitposterAttributeStatusEnum.ACTIVE.value,
        retired_by_operator=False,
        valid_from=now,
    )
    session.add(row)
    session.flush()
    _log(
        session,
        persona_id=persona_id,
        entity_kind=ShitposterMemoryEntityKindEnum.ATTRIBUTE.value,
        entity_id=row.id,
        operation=ShitposterMemoryOperationEnum.ADD,
        before_text=None,
        after_text=text,
        cause_kind=cause_kind,
        cause_post_id=cause_post_id,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
        now=now,
    )
    return row


def _supersede_attribute(
    session: Session,
    current: ShitposterPersonaAttribute,
    *,
    status: str,
    text: str,
    retired_by_operator: bool,
    now: datetime.datetime,
) -> ShitposterPersonaAttribute:
    # Close first and flush so the partial unique index never sees two current rows.
    current.valid_to = now
    session.add(current)
    session.flush()
    successor = ShitposterPersonaAttribute(
        persona_id=current.persona_id,
        attribute_key=current.attribute_key,
        text=text,
        status=status,
        retired_by_operator=retired_by_operator,
        valid_from=now,
    )
    session.add(successor)
    return successor


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
    now = _now(now)
    with SessionManager(session) as s:
        row = _add_attribute(
            s,
            persona_id=persona_id,
            attribute_key=attribute_key,
            text=text,
            cause_kind=cause_kind,
            cause_post_id=cause_post_id,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            now=now,
        )
        s.commit()
        s.refresh(row)
        return row


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
    now = _now(now)
    with SessionManager(session) as s:
        successor = _reinforce_attribute(
            s,
            attribute_id,
            cause_kind=cause_kind,
            cause_post_id=cause_post_id,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            now=now,
        )
        s.commit()
        s.refresh(successor)
        return successor


def _reinforce_attribute(
    session: Session,
    attribute_id: int,
    *,
    cause_kind: str,
    cause_post_id: Optional[int],
    cause_ref: Optional[str],
    reflector_run_id: Optional[str],
    now: datetime.datetime,
) -> ShitposterPersonaAttribute:
    _require_cause(cause_kind, cause_post_id, cause_ref)
    current = _current_attribute(session, attribute_id)
    if current.status != ShitposterAttributeStatusEnum.ACTIVE.value:
        raise ValueError(f"attribute {attribute_id} is {current.status}, not active")
    successor = _supersede_attribute(
        session,
        current,
        status=current.status,
        text=current.text,
        retired_by_operator=False,
        now=now,
    )
    _log(
        session,
        persona_id=current.persona_id,
        entity_kind=ShitposterMemoryEntityKindEnum.ATTRIBUTE.value,
        entity_id=current.id,
        operation=ShitposterMemoryOperationEnum.REINFORCE,
        before_text=current.text,
        after_text=current.text,
        cause_kind=cause_kind,
        cause_post_id=cause_post_id,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
        now=now,
    )
    return successor


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
    now = _now(now)
    with SessionManager(session) as s:
        row, _ = _retire_attribute(
            s,
            attribute_id,
            cause_kind=cause_kind,
            cause_post_id=cause_post_id,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            retired_by_operator=retired_by_operator,
            now=now,
        )
        s.commit()
        s.refresh(row)
        return row


def _retire_attribute(
    session: Session,
    attribute_id: int,
    *,
    cause_kind: str,
    cause_post_id: Optional[int],
    cause_ref: Optional[str],
    reflector_run_id: Optional[str],
    retired_by_operator: bool,
    now: datetime.datetime,
) -> tuple[ShitposterPersonaAttribute, ShitposterMemoryChange]:
    _require_cause(cause_kind, cause_post_id, cause_ref)
    current = _current_attribute(session, attribute_id)
    if current.status != ShitposterAttributeStatusEnum.ACTIVE.value:
        raise ValueError(f"attribute {attribute_id} is {current.status}, not active")
    successor = _supersede_attribute(
        session,
        current,
        status=ShitposterAttributeStatusEnum.RETIRED.value,
        text=current.text,
        retired_by_operator=retired_by_operator,
        now=now,
    )
    change = _log(
        session,
        persona_id=current.persona_id,
        entity_kind=ShitposterMemoryEntityKindEnum.ATTRIBUTE.value,
        entity_id=current.id,
        operation=ShitposterMemoryOperationEnum.RETIRE,
        before_text=current.text,
        after_text=None,
        cause_kind=cause_kind,
        cause_post_id=cause_post_id,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
        now=now,
    )
    return successor, change


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
    now = _now(now)
    with SessionManager(session) as s:
        successor = _merge_attributes(
            s,
            from_attribute_id,
            into_attribute_id,
            cause_kind=cause_kind,
            cause_post_id=cause_post_id,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            now=now,
        )
        s.commit()
        s.refresh(successor)
        return successor


def _merge_attributes(
    session: Session,
    from_attribute_id: int,
    into_attribute_id: int,
    *,
    cause_kind: str,
    cause_post_id: Optional[int],
    cause_ref: Optional[str],
    reflector_run_id: Optional[str],
    now: datetime.datetime,
) -> ShitposterPersonaAttribute:
    _require_cause(cause_kind, cause_post_id, cause_ref)
    if from_attribute_id == into_attribute_id:
        raise ValueError("cannot merge an attribute into itself")
    source = _current_attribute(session, from_attribute_id)
    target = _current_attribute(session, into_attribute_id)
    if source.persona_id != target.persona_id:
        raise ValueError("attributes belong to different personas")
    if source.status != ShitposterAttributeStatusEnum.ACTIVE.value:
        raise ValueError(f"attribute {from_attribute_id} is not active")
    if target.status != ShitposterAttributeStatusEnum.ACTIVE.value:
        raise ValueError(f"attribute {into_attribute_id} is not active")
    successor = _supersede_attribute(
        session,
        source,
        status=ShitposterAttributeStatusEnum.MERGED.value,
        text=source.text,
        retired_by_operator=False,
        now=now,
    )
    _log(
        session,
        persona_id=source.persona_id,
        entity_kind=ShitposterMemoryEntityKindEnum.ATTRIBUTE.value,
        entity_id=source.id,
        operation=ShitposterMemoryOperationEnum.MERGE,
        before_text=source.text,
        after_text=target.text,
        cause_kind=cause_kind,
        cause_post_id=cause_post_id,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
        now=now,
    )
    return successor


def _add_lore(
    session: Session,
    *,
    persona_id: int,
    text: str,
    kind: str,
    cause_kind: str,
    source_post_id: Optional[int],
    cause_post_id: Optional[int],
    cause_ref: Optional[str],
    reflector_run_id: Optional[str],
    popularity_score: int,
    now: datetime.datetime,
) -> ShitposterLoreEntry:
    if kind not in _LORE_KINDS:
        raise ValueError(f"unknown lore kind {kind!r}")
    _require_cause(cause_kind, cause_post_id, cause_ref)
    row = ShitposterLoreEntry(
        persona_id=persona_id,
        text=text,
        kind=kind,
        source_post_id=source_post_id,
        popularity_score=popularity_score,
        retired_by_operator=False,
        valid_from=now,
    )
    session.add(row)
    session.flush()
    _log(
        session,
        persona_id=persona_id,
        entity_kind=ShitposterMemoryEntityKindEnum.LORE.value,
        entity_id=row.id,
        operation=ShitposterMemoryOperationEnum.ADD,
        before_text=None,
        after_text=text,
        cause_kind=cause_kind,
        cause_post_id=cause_post_id,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
        now=now,
    )
    return row


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
    now = _now(now)
    with SessionManager(session) as s:
        row = _add_lore(
            s,
            persona_id=persona_id,
            text=text,
            kind=kind,
            cause_kind=cause_kind,
            source_post_id=source_post_id,
            cause_post_id=cause_post_id,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            popularity_score=0,
            now=now,
        )
        s.commit()
        s.refresh(row)
        return row


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
    """Close the current lore row as retired, logging a retire.

    Lore has no status column, so a retired row is simply closed; the
    retired_by_operator flag is set on that closed row in the same write.
    """
    now = _now(now)
    with SessionManager(session) as s:
        row, _ = _retire_lore(
            s,
            lore_id,
            cause_kind=cause_kind,
            cause_post_id=cause_post_id,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            retired_by_operator=retired_by_operator,
            now=now,
        )
        s.commit()
        s.refresh(row)
        return row


def _retire_lore(
    session: Session,
    lore_id: int,
    *,
    cause_kind: str,
    cause_post_id: Optional[int],
    cause_ref: Optional[str],
    reflector_run_id: Optional[str],
    retired_by_operator: bool,
    now: datetime.datetime,
) -> tuple[ShitposterLoreEntry, ShitposterMemoryChange]:
    _require_cause(cause_kind, cause_post_id, cause_ref)
    current = _current_lore(session, lore_id)
    current.valid_to = now
    current.retired_by_operator = retired_by_operator
    session.add(current)
    change = _log(
        session,
        persona_id=current.persona_id,
        entity_kind=ShitposterMemoryEntityKindEnum.LORE.value,
        entity_id=current.id,
        operation=ShitposterMemoryOperationEnum.RETIRE,
        before_text=current.text,
        after_text=None,
        cause_kind=cause_kind,
        cause_post_id=cause_post_id,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
        now=now,
    )
    return current, change


def fold_lore(
    from_lore_ids: list[int],
    into_text: str,
    cause_ref: str,
    reflector_run_id: Optional[str] = None,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterLoreEntry:
    """Retire several lore rows into one consolidated entry, logging folds."""
    if not from_lore_ids:
        raise ValueError("fold requires at least one source lore id")
    if not cause_ref:
        raise ValueError("fold requires a cause_ref")
    now = _now(now)
    with SessionManager(session) as s:
        consolidated = fold_items(
            s,
            lore_ids=from_lore_ids,
            attribute_ids=[],
            into_text=into_text,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            now=now,
        )
        s.commit()
        s.refresh(consolidated)
        return consolidated


def current_consolidated_lore(
    persona_id: int, session: Optional[Session] = None
) -> Optional[ShitposterLoreEntry]:
    """The persona's current consolidated lore entry, if one exists."""
    with SessionManager(session) as s:
        return s.exec(
            select(ShitposterLoreEntry)
            .where(ShitposterLoreEntry.persona_id == persona_id)
            .where(ShitposterLoreEntry.valid_to.is_(None))
            .where(ShitposterLoreEntry.kind == ShitposterLoreKindEnum.CONSOLIDATED.value)
        ).one_or_none()


def fold_items(
    session: Session,
    *,
    lore_ids: list[int],
    attribute_ids: list[int],
    into_text: str,
    cause_ref: str,
    reflector_run_id: Optional[str],
    now: datetime.datetime,
) -> ShitposterLoreEntry:
    """Fold source rows into one new consolidated lore entry, inside the caller's transaction.

    Current lore sources are closed. Attribute sources must already be closed as
    retired or merged, and an operator-retired attribute is refused. Each source
    gets a fold change row; the caller owns the commit.
    """
    if not lore_ids and not attribute_ids:
        raise ValueError("fold requires at least one source")
    if not cause_ref:
        raise ValueError("fold requires a cause_ref")
    lore_rows = [_current_lore(session, i) for i in sorted(set(lore_ids))]
    attribute_rows = [_fold_attribute_source(session, i) for i in sorted(set(attribute_ids))]
    persona_ids = {row.persona_id for row in lore_rows + attribute_rows}
    if len(persona_ids) != 1:
        raise ValueError("fold sources must belong to one persona")
    persona_id = persona_ids.pop()
    cause = ShitposterMemoryCauseKindEnum.FOLD.value

    for row in lore_rows:
        row.valid_to = now
        session.add(row)
    session.flush()
    for row in lore_rows:
        _log(
            session,
            persona_id=persona_id,
            entity_kind=ShitposterMemoryEntityKindEnum.LORE.value,
            entity_id=row.id,
            operation=ShitposterMemoryOperationEnum.FOLD,
            before_text=row.text,
            after_text=into_text,
            cause_kind=cause,
            cause_post_id=None,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            now=now,
        )
    for row in attribute_rows:
        _log(
            session,
            persona_id=persona_id,
            entity_kind=ShitposterMemoryEntityKindEnum.ATTRIBUTE.value,
            entity_id=row.id,
            operation=ShitposterMemoryOperationEnum.FOLD,
            before_text=row.text,
            after_text=into_text,
            cause_kind=cause,
            cause_post_id=None,
            cause_ref=cause_ref,
            reflector_run_id=reflector_run_id,
            now=now,
        )
    consolidated = ShitposterLoreEntry(
        persona_id=persona_id,
        text=into_text,
        kind=ShitposterLoreKindEnum.CONSOLIDATED.value,
        source_post_id=None,
        popularity_score=sum(row.popularity_score for row in lore_rows),
        retired_by_operator=False,
        valid_from=now,
    )
    session.add(consolidated)
    session.flush()
    _log(
        session,
        persona_id=persona_id,
        entity_kind=ShitposterMemoryEntityKindEnum.LORE.value,
        entity_id=consolidated.id,
        operation=ShitposterMemoryOperationEnum.ADD,
        before_text=None,
        after_text=into_text,
        cause_kind=cause,
        cause_post_id=None,
        cause_ref=cause_ref,
        reflector_run_id=reflector_run_id,
        now=now,
    )
    return consolidated


def _fold_attribute_source(session: Session, attribute_id: int) -> ShitposterPersonaAttribute:
    row = session.get(ShitposterPersonaAttribute, attribute_id)
    if row is None:
        raise LookupError(f"no attribute with id {attribute_id}")
    # retire and merge close the source row but leave its status as written at add time
    if row.valid_to is None:
        raise ValueError(f"attribute {attribute_id} is still current; only retired or merged attributes fold")
    if row.retired_by_operator:
        raise ValueError(f"attribute {attribute_id} was retired by the operator and never folds")
    return row


def operator_retire(
    entity_kind: str,
    entity_id: int,
    operator_subject: str,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterMemoryChange:
    """Operator retire of an attribute or lore row; cause_kind is operator."""
    if entity_kind not in _ENTITY_KINDS:
        raise ValueError(f"unknown entity_kind {entity_kind!r}")
    if not operator_subject:
        raise ValueError("operator retire requires the operator subject")
    now = _now(now)
    cause = ShitposterMemoryCauseKindEnum.OPERATOR.value
    with SessionManager(session) as s:
        if entity_kind == ShitposterMemoryEntityKindEnum.ATTRIBUTE.value:
            _, change = _retire_attribute(
                s,
                entity_id,
                cause_kind=cause,
                cause_post_id=None,
                cause_ref=operator_subject,
                reflector_run_id=None,
                retired_by_operator=True,
                now=now,
            )
        else:
            _, change = _retire_lore(
                s,
                entity_id,
                cause_kind=cause,
                cause_post_id=None,
                cause_ref=operator_subject,
                reflector_run_id=None,
                retired_by_operator=True,
                now=now,
            )
        s.commit()
        s.refresh(change)
        return change


def seed_persona_attributes(
    persona_id: int,
    persona_text: str,
    cause_ref: str = SEED_CAUSE_REF,
    now: Optional[datetime.datetime] = None,
    session: Optional[Session] = None,
) -> ShitposterPersonaAttribute:
    """Seed the base attribute from the fixed persona text, once per persona.

    Returns the existing seed row when one is already current.
    """
    now = _now(now)
    with SessionManager(session) as s:
        existing = s.exec(
            select(ShitposterPersonaAttribute)
            .where(ShitposterPersonaAttribute.persona_id == persona_id)
            .where(ShitposterPersonaAttribute.attribute_key == SEED_ATTRIBUTE_KEY)
            .where(ShitposterPersonaAttribute.valid_to.is_(None))
        ).one_or_none()
        if existing is not None:
            return existing
        row = _add_attribute(
            s,
            persona_id=persona_id,
            attribute_key=SEED_ATTRIBUTE_KEY,
            text=persona_text,
            cause_kind=ShitposterMemoryCauseKindEnum.OPERATOR.value,
            cause_post_id=None,
            cause_ref=cause_ref,
            reflector_run_id=None,
            now=now,
        )
        s.commit()
        s.refresh(row)
        return row


def active_attributes(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterPersonaAttribute]:
    """Current attributes for a persona."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterPersonaAttribute)
                .where(ShitposterPersonaAttribute.persona_id == persona_id)
                .where(ShitposterPersonaAttribute.valid_to.is_(None))
                .where(
                    ShitposterPersonaAttribute.status
                    == ShitposterAttributeStatusEnum.ACTIVE.value
                )
                .order_by(ShitposterPersonaAttribute.id)
            ).all()
        )


def active_lore(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterLoreEntry]:
    """Current lore entries for a persona."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterLoreEntry)
                .where(ShitposterLoreEntry.persona_id == persona_id)
                .where(ShitposterLoreEntry.valid_to.is_(None))
                .order_by(ShitposterLoreEntry.id)
            ).all()
        )


def change_history(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterMemoryChange]:
    """Change log for a persona, newest first."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterMemoryChange)
                .where(ShitposterMemoryChange.persona_id == persona_id)
                .order_by(
                    ShitposterMemoryChange.changed_at.desc(),  # type: ignore[union-attr]
                    ShitposterMemoryChange.id.desc(),  # type: ignore[union-attr]
                )
            ).all()
        )


def attributes_as_of(
    persona_id: int, as_of: datetime.datetime, session: Optional[Session] = None
) -> list[ShitposterPersonaAttribute]:
    """Point-in-time attribute rows for a persona."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterPersonaAttribute)
                .where(ShitposterPersonaAttribute.persona_id == persona_id)
                .where(ShitposterPersonaAttribute.valid_from <= as_of)
                .where(
                    or_(
                        ShitposterPersonaAttribute.valid_to.is_(None),  # type: ignore[union-attr]
                        ShitposterPersonaAttribute.valid_to > as_of,  # type: ignore[operator]
                    )
                )
                .order_by(ShitposterPersonaAttribute.attribute_key, ShitposterPersonaAttribute.id)
            ).all()
        )


def list_reflector_runs(
    persona_id: int,
    limit: int,
    offset: int = 0,
    session: Optional[Session] = None,
) -> list[tuple[ShitposterReflectorRun, datetime.datetime]]:
    """Reflector run records with their brain job start time, newest first."""
    with SessionManager(session) as s:
        rows = s.exec(
            select(ShitposterReflectorRun, ShitposterBrainJobRun.started_at)
            .join(
                ShitposterBrainJobRun,
                ShitposterBrainJobRun.id == ShitposterReflectorRun.brain_job_run_id,
            )
            .where(ShitposterReflectorRun.persona_id == persona_id)
            .order_by(
                ShitposterBrainJobRun.started_at.desc(),  # type: ignore[union-attr]
                ShitposterReflectorRun.id.desc(),  # type: ignore[union-attr]
            )
            .offset(offset)
            .limit(limit)
        ).all()
        return [(run, started_at) for run, started_at in rows]


def count_reflector_runs(persona_id: int, session: Optional[Session] = None) -> int:
    with SessionManager(session) as s:
        return s.exec(
            select(func.count())
            .select_from(ShitposterReflectorRun)
            .where(ShitposterReflectorRun.persona_id == persona_id)
        ).one()


def get_post_engagement(
    post_id: int, session: Optional[Session] = None
) -> ShitposterPostEngagement | None:
    with SessionManager(session) as s:
        return s.get(ShitposterPostEngagement, post_id)


def get_suggestion(
    suggestion_id: int, session: Optional[Session] = None
) -> ShitposterSuggestion | None:
    with SessionManager(session) as s:
        return s.get(ShitposterSuggestion, suggestion_id)


def active_backer_slack_ids(
    suggestion_id: int, session: Optional[Session] = None
) -> list[str]:
    """Slack user ids currently backing a suggestion, in backing order."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterSuggestionBacker.slack_user_id)
                .where(ShitposterSuggestionBacker.suggestion_id == suggestion_id)
                .where(ShitposterSuggestionBacker.removed_at.is_(None))  # type: ignore[union-attr]
                .order_by(
                    ShitposterSuggestionBacker.backed_at,  # type: ignore[union-attr]
                    ShitposterSuggestionBacker.id,  # type: ignore[union-attr]
                )
            ).all()
        )
