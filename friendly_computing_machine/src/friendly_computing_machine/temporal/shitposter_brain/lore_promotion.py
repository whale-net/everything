"""Promote popular bot posts into lore entries.

Runs as a post-step of the harvest apply, in the harvest run's transaction. For
each newly finalized engagement row (consumed_by_lore_run_id NULL), a post whose
popularity ranks in the top share of finalized posts from the last window is
inserted as a `hit` lore entry via the memory DAL (cause_kind=post_engagement),
then the engagement row is marked consumed by this run.

Never promoted: a post with more negative than other reactions, a post that
targets a named member, or a post whose earlier lore entry the operator retired.
Lore is not removed when the post leaves the window.
"""

import datetime
import logging
import math
from dataclasses import dataclass

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal.shitposter_memory_dal import (
    _add_lore,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobStatus,
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterLoreKindEnum,
    ShitposterMemoryCauseKindEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.guardrails import (
    find_member_name,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
)
from friendly_computing_machine.src.friendly_computing_machine.util import (
    ts_to_datetime,
)

logger = logging.getLogger(__name__)

DEFAULT_TOP_SHARE = 0.10
DEFAULT_WINDOW_DAYS = 30


@dataclass(frozen=True)
class PromotionCandidate:
    post_id: int
    persona_id: int
    popularity_score: int
    negative_dominant: bool
    targets_named_member: bool


def popularity_score(distinct_reactors: int, distinct_repliers: int) -> int:
    """Lifetime popularity of a finalized post: reactors plus repliers."""
    return distinct_reactors + distinct_repliers


def cutoff_rank_count(finalized_in_window: int, top_share: float) -> int:
    """How many of the window's finalized posts fall in the top share (at least 1 if any)."""
    if finalized_in_window <= 0:
        return 0
    if not 0 < top_share <= 1:
        raise ValueError(f"top_share must be in (0, 1], got {top_share}")
    # the epsilon keeps float error (e.g. 30 * 0.1 = 3.0000000000000004) from adding a rank
    return max(1, math.ceil(finalized_in_window * top_share - 1e-9))


def _utc(dt: datetime.datetime) -> datetime.datetime:
    # sqlite returns naive datetimes; stored timestamps are UTC
    if dt.tzinfo is not None:
        return dt
    return dt.replace(tzinfo=datetime.timezone.utc)


def _member_names(session: Session) -> list[str]:
    names = session.exec(
        select(SlackUser.name).where(SlackUser.is_bot.is_(False))  # type: ignore[attr-defined]
    ).all()
    return [n for n in names if n]


def _post_text(session: Session, post_id: int) -> str | None:
    post_ts, channel_slack_id = session.exec(
        select(ShitposterPost.slack_message_ts, SlackChannel.slack_id)
        .join(SlackChannel, SlackChannel.id == ShitposterPost.slack_channel_id)  # type: ignore[arg-type]
        .where(ShitposterPost.id == post_id)
    ).one()
    return session.exec(
        select(SlackMessage.text)
        .where(SlackMessage.slack_channel_slack_id == channel_slack_id)
        .where(SlackMessage.ts == ts_to_datetime(post_ts))  # type: ignore[arg-type]
    ).first()


def _operator_retired_before(session: Session, post_id: int) -> bool:
    return (
        session.exec(
            select(ShitposterLoreEntry.id)
            .where(ShitposterLoreEntry.source_post_id == post_id)
            .where(ShitposterLoreEntry.retired_by_operator.is_(True))  # type: ignore[attr-defined]
        ).first()
        is not None
    )


def promote_new_lore(
    session: Session,
    run_id: int,
    persona_id: int,
    now: datetime.datetime,
    top_share: float = DEFAULT_TOP_SHARE,
    window_days: int = DEFAULT_WINDOW_DAYS,
) -> ApplyOutcome:
    """Insert lore for newly finalized popular posts and mark their engagement consumed."""
    window_start = now - datetime.timedelta(days=window_days)
    engagement = list(
        session.exec(
            select(ShitposterPostEngagement)
            .where(ShitposterPostEngagement.persona_id == persona_id)
            .order_by(ShitposterPostEngagement.post_id)  # type: ignore[arg-type]
        ).all()
    )
    # ties broken by post id so reruns rank the same way
    in_window = [e for e in engagement if _utc(e.finalized_at) >= window_start]
    ranked = sorted(
        in_window,
        key=lambda e: (
            -popularity_score(e.distinct_reactors, e.distinct_repliers),
            e.post_id,
        ),
    )
    top_ids = {
        e.post_id for e in ranked[: cutoff_rank_count(len(ranked), top_share)]
    }

    pending = [e for e in engagement if e.consumed_by_lore_run_id is None]
    names = _member_names(session)
    excluded = {"negative_dominant": 0, "named_member": 0, "operator_retired": 0, "no_text": 0}
    promoted = 0
    for row in pending:
        if row.post_id not in top_ids:
            row.consumed_by_lore_run_id = run_id
            session.add(row)
            continue
        score = popularity_score(row.distinct_reactors, row.distinct_repliers)
        other = sum(row.reactions_by_emoji.values()) - row.negative_reactions
        if row.negative_reactions > other:
            excluded["negative_dominant"] += 1
        else:
            text = _post_text(session, row.post_id)
            if text is None:
                excluded["no_text"] += 1
            elif find_member_name(text, names):
                excluded["named_member"] += 1
            elif _operator_retired_before(session, row.post_id):
                excluded["operator_retired"] += 1
            else:
                _add_lore(
                    session,
                    persona_id=persona_id,
                    text=text,
                    kind=ShitposterLoreKindEnum.HIT.value,
                    cause_kind=ShitposterMemoryCauseKindEnum.POST_ENGAGEMENT.value,
                    source_post_id=row.post_id,
                    cause_post_id=row.post_id,
                    cause_ref=None,
                    reflector_run_id=None,
                    popularity_score=score,
                    now=now,
                )
                promoted += 1
        row.consumed_by_lore_run_id = run_id
        session.add(row)
    session.flush()
    details = {"consumed": len(pending), "promoted": promoted, "excluded": excluded}
    logger.info("lore promotion for persona=%s: %s", persona_id, details)
    status = ShitposterBrainJobStatus.SUCCEEDED if promoted else ShitposterBrainJobStatus.NO_OP
    return ApplyOutcome(status=status.value, details=details)
