"""Shitposter source-weight DAL: per-persona SCD2 target weights for the
theme and novel lore sources. Each revision closes the current row and opens
a new one carrying its cause.
"""

import datetime
from typing import Any, Optional

from sqlmodel import Session

from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterSourceWeight,
    ShitposterWeightSourceEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    SourceWeightConfig,
)


def get_current_weights(
    persona_id: int,
    session: Optional[Session] = None,
    config: Optional[SourceWeightConfig] = None,
) -> dict[str, ShitposterSourceWeight]:
    """Current weight row per source; seeds missing sources from the configured starting weights."""
    raise NotImplementedError


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
    raise NotImplementedError


def list_weight_history(
    persona_id: int, session: Optional[Session] = None
) -> list[ShitposterSourceWeight]:
    """Every weight revision for a persona, oldest first."""
    raise NotImplementedError
