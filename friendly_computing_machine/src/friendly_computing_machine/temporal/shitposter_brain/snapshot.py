"""Snapshot brain job: ranked persona memory rendered within a token budget.

Attributes go in first, in id order. Lore follows, ranked by a score of
recency decay plus lifetime popularity, bounded to the top N (the ranked
section). One lore entry ranked below N is then picked at random if it fits. Every item is checked against the budget against
the full rendered text, so a pick is dropped rather than overflowing.

Suggestion text is never read here; only attributes derived from it are.
"""

import datetime
import logging
import math
import random
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from sqlalchemy import func
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal.shitposter_memory_dal import (
    active_attributes,
    active_lore,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobStatus,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    DEFAULT_SNAPSHOT_RANKED_LORE_CAP,
    load_context_token_budget,
    load_lore_decay_half_life_hours,
    load_snapshot_ranked_lore_cap,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
    BrainJobInput,
    JobBody,
    register_job_body,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.fold import (
    fold_stale_lore,
)

logger = logging.getLogger(__name__)

ITEM_ATTRIBUTE = "attribute"
ITEM_LORE = "lore"
CHARS_PER_TOKEN = 4

# Tests replace this to make the random pick reproducible.
RNG_FACTORY: Callable[[], random.Random] = random.Random


@dataclass(frozen=True)
class Candidate:
    item_kind: str
    item_id: int
    text: str
    # lore only; higher ranks first
    score: float = 0.0


@dataclass(frozen=True)
class SelectedItem:
    item_kind: str
    item_id: int
    text: str
    rank: int
    is_random_pick: bool


@dataclass(frozen=True)
class Snapshot:
    rendered_text: str
    token_count: int
    items: list[SelectedItem]


def approx_tokens(text: str) -> int:
    """Deterministic token estimate: one token per four characters, rounded up."""
    return math.ceil(len(text) / CHARS_PER_TOKEN)


def render_line(text: str) -> str:
    return f"- {text}"


def lore_score(
    popularity_score: int,
    last_changed_at: datetime.datetime,
    now: datetime.datetime,
    half_life_hours: float,
) -> float:
    """Recency decay in (0, 1] plus popularity share in [0, 1)."""
    age_hours = max(0.0, (now - last_changed_at).total_seconds() / 3600)
    recency = 0.5 ** (age_hours / half_life_hours)
    popularity = max(0, popularity_score) / (max(0, popularity_score) + 1)
    return recency + popularity


def select_items(
    attributes: list[Candidate],
    lore: list[Candidate],
    budget: int,
    rng: random.Random,
    ranked_lore_cap: int = DEFAULT_SNAPSHOT_RANKED_LORE_CAP,
) -> Snapshot:
    """Fill attributes, then the top-N ranked lore, then one random pick from the rest."""
    lines: list[str] = []
    chosen: list[SelectedItem] = []
    included: set[tuple[str, int]] = set()

    def fits(line: str) -> bool:
        return approx_tokens("\n".join([*lines, line])) <= budget

    def take(candidate: Candidate, is_random: bool) -> bool:
        line = render_line(candidate.text)
        if not fits(line):
            return False
        lines.append(line)
        chosen.append(
            SelectedItem(
                item_kind=candidate.item_kind,
                item_id=candidate.item_id,
                text=candidate.text,
                rank=len(chosen) + 1,
                is_random_pick=is_random,
            )
        )
        included.add((candidate.item_kind, candidate.item_id))
        return True

    dropped = [a for a in attributes if not take(a, False)]
    if dropped:
        logger.warning(
            "context snapshot dropped %d attribute(s) to fit the %d token budget",
            len(dropped),
            budget,
        )

    ranked_lore = sorted(lore, key=lambda c: (-c.score, c.item_id))
    ranked_section = ranked_lore[:ranked_lore_cap]
    for candidate in ranked_section:
        take(candidate, False)

    pool = [
        c
        for c in ranked_lore[ranked_lore_cap:]
        if (c.item_kind, c.item_id) not in included and fits(render_line(c.text))
    ]
    if pool:
        take(rng.choice(pool), True)

    rendered = "\n".join(lines)
    return Snapshot(
        rendered_text=rendered,
        token_count=approx_tokens(rendered),
        items=chosen,
    )


def _aware(value: datetime.datetime) -> datetime.datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=datetime.timezone.utc)
    return value


def _compute(params: BrainJobInput) -> dict[str, Any]:
    # the snapshot reads and writes in the apply transaction, so compute only carries the persona
    return {"persona_id": params.persona_id}


def _apply(session: Session, run_id: int, payload: dict[str, Any]) -> ApplyOutcome:
    persona_id = int(payload["persona_id"])
    now = datetime.datetime.now(datetime.timezone.utc)
    budget = load_context_token_budget()
    half_life = load_lore_decay_half_life_hours()
    ranked_cap = load_snapshot_ranked_lore_cap()

    # folds before the candidate read so stale lore leaves the pool in this same transaction
    fold_stale_lore(persona_id, now=now, session=session)

    attributes = [
        Candidate(ITEM_ATTRIBUTE, row.id, row.text)
        for row in active_attributes(persona_id, session=session)
    ]
    lore = [
        Candidate(
            ITEM_LORE,
            row.id,
            row.text,
            lore_score(row.popularity_score, _aware(row.valid_from), now, half_life),
        )
        for row in active_lore(persona_id, session=session)
    ]

    snapshot = select_items(attributes, lore, budget, RNG_FACTORY(), ranked_cap)

    latest = session.exec(
        select(func.max(ShitposterContextSnapshot.version)).where(
            ShitposterContextSnapshot.persona_id == persona_id
        )
    ).one()
    version = (latest or 0) + 1
    row = ShitposterContextSnapshot(
        persona_id=persona_id,
        version=version,
        created_at=now,
        brain_job_run_id=run_id,
        token_count=snapshot.token_count,
        rendered_text=snapshot.rendered_text,
    )
    session.add(row)
    session.flush()
    for item in snapshot.items:
        session.add(
            ShitposterSnapshotItem(
                snapshot_id=row.id,
                item_kind=item.item_kind,
                item_id=item.item_id,
                rank=item.rank,
                is_random_pick=item.is_random_pick,
            )
        )
    logger.info(
        "context snapshot written: persona=%s version=%s tokens=%s items=%s",
        persona_id,
        version,
        snapshot.token_count,
        len(snapshot.items),
    )
    return ApplyOutcome(
        status=ShitposterBrainJobStatus.SUCCEEDED.value,
        details={
            "snapshot_id": row.id,
            "version": version,
            "token_count": snapshot.token_count,
            "item_count": len(snapshot.items),
        },
    )


register_job_body(
    ShitposterBrainJobKind.SNAPSHOT,
    JobBody(compute=_compute, apply=_apply),
)
