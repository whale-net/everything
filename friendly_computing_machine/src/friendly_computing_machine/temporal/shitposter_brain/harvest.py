"""Harvest brain job: finalize each bot post's engagement 24h after it was posted.

Compute reads captured reactions and thread replies and writes nothing. Apply
inserts one write-once ShitposterPostEngagement row per newly finalized post in
the runner's transaction; the post_id primary key keeps a row from being
rewritten or duplicated, so later feedback never changes a finalized record.
"""

import datetime
import logging
from typing import Any

from sqlmodel import Session, select
from temporalio.client import Client

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobStatus,
    ShitposterChannelOptIn,
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack_reaction import (
    SlackReaction,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    load_lore_top_share,
    load_lore_window_days,
    load_negative_emoji,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
    BrainJobInput,
    JobBody,
    register_job_body,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.control import (
    register_brain_schedule,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.lore_promotion import (
    promote_new_lore,
)
from friendly_computing_machine.src.friendly_computing_machine.util import (
    ts_to_datetime,
)

logger = logging.getLogger(__name__)

# a post is finalized once it is at least this old
FINALIZE_AFTER = datetime.timedelta(hours=24)
HARVEST_SCHEDULE_EVERY = datetime.timedelta(hours=1)


def _utc(dt: datetime.datetime | None) -> datetime.datetime | None:
    # sqlite returns naive datetimes; captured timestamps are UTC
    if dt is None or dt.tzinfo is not None:
        return dt
    return dt.replace(tzinfo=datetime.timezone.utc)


def _active_at(row: SlackReaction, at: datetime.datetime) -> bool:
    """A reaction counts at `at` if it was added by then and not removed before it."""
    added = _utc(row.added_at)
    removed = _utc(row.removed_at)
    if added is not None and added > at:
        return False
    return removed is None or removed > at


def _bot_slack_ids(session: Session) -> set[str]:
    return set(
        session.exec(
            select(SlackUser.slack_id).where(SlackUser.is_bot.is_(True))  # type: ignore[attr-defined]
        ).all()
    )


def _reactions_at(
    session: Session,
    channel_slack_id: str,
    message_ts: str,
    bots: set[str],
    at: datetime.datetime,
) -> dict[str, set[str]]:
    """Human reacting users per emoji, net of reactions removed before `at`."""
    users_by_emoji: dict[str, set[str]] = {}
    rows = session.exec(
        select(SlackReaction).where(
            SlackReaction.slack_channel_slack_id == channel_slack_id,
            SlackReaction.message_ts == message_ts,
        )
    ).all()
    for row in rows:
        if row.is_bot or row.slack_user_slack_id in bots:
            continue
        if not _active_at(row, at):
            continue
        users_by_emoji.setdefault(row.emoji, set()).add(row.slack_user_slack_id)
    return users_by_emoji


def _distinct_repliers(
    session: Session, channel_slack_id: str, message_ts: str, bots: set[str]
) -> int:
    users = session.exec(
        select(SlackMessage.slack_user_slack_id).where(
            SlackMessage.slack_channel_slack_id == channel_slack_id,
            SlackMessage.thread_ts == ts_to_datetime(message_ts),  # type: ignore[arg-type]
        )
    ).all()
    return len({u for u in users if u and u not in bots})


def _compute(params: BrainJobInput) -> dict[str, Any]:
    now = datetime.datetime.now(datetime.timezone.utc)
    cutoff = now - FINALIZE_AFTER
    negative = load_negative_emoji()
    with SessionManager() as session:
        opted_in = set(
            session.exec(
                select(SlackChannel.slack_id)
                .join(
                    ShitposterChannelOptIn,
                    SlackChannel.id == ShitposterChannelOptIn.slack_channel_id,  # type: ignore[arg-type]
                )
                .where(ShitposterChannelOptIn.opted_in.is_(True))  # type: ignore[attr-defined]
                .where(ShitposterChannelOptIn.valid_to.is_(None))  # type: ignore[union-attr]
            ).all()
        )
        candidates = session.exec(
            select(ShitposterPost, SlackChannel.slack_id)
            .join(SlackChannel, SlackChannel.id == ShitposterPost.slack_channel_id)  # type: ignore[arg-type]
            .outerjoin(
                ShitposterPostEngagement,
                ShitposterPostEngagement.post_id == ShitposterPost.id,  # type: ignore[arg-type]
            )
            .where(ShitposterPost.persona_id == params.persona_id)
            .where(ShitposterPostEngagement.post_id.is_(None))  # type: ignore[union-attr]
            .order_by(ShitposterPost.id)  # type: ignore[arg-type]
        ).all()
        bots = _bot_slack_ids(session)

        records: list[dict[str, Any]] = []
        for post, channel_slack_id in candidates:
            if channel_slack_id not in opted_in or _utc(post.created_at) > cutoff:
                continue
            users_by_emoji = _reactions_at(
                session, channel_slack_id, post.slack_message_ts, bots, now
            )
            reactors = set().union(*users_by_emoji.values()) if users_by_emoji else set()
            by_emoji = {e: len(u) for e, u in users_by_emoji.items()}
            records.append(
                {
                    "post_id": post.id,
                    "persona_id": post.persona_id,
                    "distinct_reactors": len(reactors),
                    "reactions_by_emoji": by_emoji,
                    "distinct_repliers": _distinct_repliers(
                        session, channel_slack_id, post.slack_message_ts, bots
                    ),
                    "negative_reactions": sum(
                        n for e, n in by_emoji.items() if e.lower() in negative
                    ),
                }
            )
    logger.info(
        "harvest computed %d engagement record(s) for persona=%s",
        len(records),
        params.persona_id,
    )
    return {
        "finalized_at": now.isoformat(),
        "persona_id": params.persona_id,
        "records": records,
    }


def _apply(session: Session, run_id: int, payload: dict[str, Any]) -> ApplyOutcome:
    finalized_at = datetime.datetime.fromisoformat(payload["finalized_at"])
    records = payload.get("records") or []
    ids = [r["post_id"] for r in records]
    existing: set[int] = set()
    if ids:
        existing = set(
            session.exec(
                select(ShitposterPostEngagement.post_id).where(
                    ShitposterPostEngagement.post_id.in_(ids)  # type: ignore[attr-defined]
                )
            ).all()
        )
    written = 0
    for rec in records:
        if rec["post_id"] in existing:
            continue
        session.add(
            ShitposterPostEngagement(
                post_id=rec["post_id"],
                persona_id=rec["persona_id"],
                finalized_at=finalized_at,
                distinct_reactors=rec["distinct_reactors"],
                reactions_by_emoji=rec["reactions_by_emoji"],
                distinct_repliers=rec["distinct_repliers"],
                negative_reactions=rec["negative_reactions"],
            )
        )
        written += 1
    session.flush()
    lore = promote_new_lore(
        session,
        run_id,
        payload["persona_id"],
        finalized_at,
        top_share=load_lore_top_share(),
        window_days=load_lore_window_days(),
    )
    details = {"finalized": written, "already_finalized": len(records) - written}
    status = (
        ShitposterBrainJobStatus.SUCCEEDED
        if written or lore.status == ShitposterBrainJobStatus.SUCCEEDED.value
        else ShitposterBrainJobStatus.NO_OP
    )
    return ApplyOutcome(status=status.value, details=details)


async def register_harvest_schedule(
    client: Client,
    task_queue: str,
    persona_id: int,
    app_env: str | None = None,
) -> None:
    """Create the hourly harvest schedule for one persona; no-op if it exists."""
    await register_brain_schedule(
        client,
        task_queue,
        persona_id,
        ShitposterBrainJobKind.HARVEST.value,
        every=HARVEST_SCHEDULE_EVERY,
        app_env=app_env,
    )


register_job_body(
    ShitposterBrainJobKind.HARVEST,
    JobBody(compute=_compute, apply=_apply),
)
