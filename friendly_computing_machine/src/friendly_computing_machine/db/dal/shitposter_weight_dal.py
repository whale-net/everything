"""Shitposter source-weight DAL: per-persona SCD2 target weights for the
theme and novel lore sources. Each revision closes the current row and opens
a new one carrying its cause.
"""

import datetime
import logging
from typing import Any, Optional

from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterSourceWeight,
    ShitposterWeightSourceEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    SourceWeightConfig,
    load_source_weight_config,
)

logger = logging.getLogger(__name__)

CAUSE_INITIAL = "initial"
CAUSE_FALLBACK = "fallback"

_SOURCES = tuple(s.value for s in ShitposterWeightSourceEnum)


def _now(now: Optional[datetime.datetime]) -> datetime.datetime:
    return now or datetime.datetime.now(datetime.UTC)


def _source_value(source: ShitposterWeightSourceEnum | str) -> str:
    value = source.value if isinstance(source, ShitposterWeightSourceEnum) else source
    if value not in _SOURCES:
        raise ValueError(f"unknown weight source {value!r}")
    return value


def _current_rows(session: Session, persona_id: int) -> dict[str, ShitposterSourceWeight]:
    rows = session.exec(
        select(ShitposterSourceWeight).where(
            ShitposterSourceWeight.persona_id == persona_id,
            ShitposterSourceWeight.valid_to.is_(None),  # type: ignore[union-attr]
        )
    ).all()
    return {r.source: r for r in rows}


def _seed_cause(config: SourceWeightConfig) -> dict[str, Any]:
    return {
        "kind": CAUSE_FALLBACK if config.fell_back else CAUSE_INITIAL,
        "theme_start": config.theme_start,
        "novel_start": config.novel_start,
    }


def _seed_missing(
    session: Session,
    persona_id: int,
    current: dict[str, ShitposterSourceWeight],
    config: SourceWeightConfig,
) -> None:
    starts = {
        ShitposterWeightSourceEnum.THEME.value: config.theme_start,
        ShitposterWeightSourceEnum.NOVEL.value: config.novel_start,
    }
    for source in _SOURCES:
        if source in current:
            continue
        row = ShitposterSourceWeight(
            persona_id=persona_id,
            source=source,
            weight=starts[source],
            cause=_seed_cause(config),
            valid_from=_now(None),
        )
        session.add(row)
        current[source] = row
    session.flush()


def get_current_weights(
    persona_id: int,
    session: Optional[Session] = None,
    config: Optional[SourceWeightConfig] = None,
) -> dict[str, ShitposterSourceWeight]:
    """Current weight row per source; seeds missing sources from the configured starting weights.

    A caller-owned session gets the seed rows flushed, not committed; the caller commits.
    """
    with SessionManager(session) as s:
        current = _current_rows(s, persona_id)
        if len(current) == len(_SOURCES):
            return current
        cfg = config or load_source_weight_config()
        if session is not None:
            _seed_missing(s, persona_id, current, cfg)
            return current
        try:
            _seed_missing(s, persona_id, current, cfg)
            s.commit()
        except IntegrityError:
            # a concurrent reader seeded first; its rows are the current ones
            s.rollback()
            current = _current_rows(s, persona_id)
        for row in current.values():
            s.refresh(row)
        logger.info("seeded source weights for persona %s", persona_id)
        return current


def revise_weight(
    session: Session,
    persona_id: int,
    source: ShitposterWeightSourceEnum | str,
    new_weight: float,
    cause: dict[str, Any],
    run_id: Optional[int] = None,
    now: Optional[datetime.datetime] = None,
) -> ShitposterSourceWeight:
    """Close the source's current weight row and open a new one with this cause."""
    if not cause:
        raise ValueError("weight revision requires a cause")
    value = _source_value(source)
    when = _now(now)
    current = session.exec(
        select(ShitposterSourceWeight).where(
            ShitposterSourceWeight.persona_id == persona_id,
            ShitposterSourceWeight.source == value,
            ShitposterSourceWeight.valid_to.is_(None),  # type: ignore[union-attr]
        )
    ).first()
    if current is not None:
        # close first and flush so the partial unique index never sees two current rows
        current.valid_to = when
        session.add(current)
        session.flush()
    successor = ShitposterSourceWeight(
        persona_id=persona_id,
        source=value,
        weight=new_weight,
        cause=cause,
        brain_job_run_id=run_id,
        valid_from=when,
    )
    session.add(successor)
    session.flush()
    return successor


def list_weight_history(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterSourceWeight]:
    """Every weight revision for a persona, oldest first."""
    with SessionManager(session) as s:
        return list(
            s.exec(
                select(ShitposterSourceWeight)
                .where(ShitposterSourceWeight.persona_id == persona_id)
                .order_by(
                    ShitposterSourceWeight.valid_from,  # type: ignore[arg-type]
                    ShitposterSourceWeight.id,  # type: ignore[arg-type]
                )
            ).all()
        )
