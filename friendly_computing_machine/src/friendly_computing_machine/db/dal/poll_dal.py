"""Ad-hoc `/poll` DAL functions."""

import datetime
import logging
from dataclasses import dataclass, field
from enum import StrEnum

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
    PollOption,
    PollVote,
)

logger = logging.getLogger(__name__)


class VoteOutcome(StrEnum):
    ADDED = "added"
    REMOVED = "removed"
    # limit 1 polls move the vote instead of rejecting the click
    SWITCHED = "switched"
    LIMIT_REACHED = "limit_reached"
    CLOSED = "closed"
    NOT_FOUND = "not_found"


@dataclass
class PollOptionTally:
    option: PollOption
    # Slack user IDs in vote order
    voters: list[str] = field(default_factory=list)


@dataclass
class PollSnapshot:
    poll: Poll
    options: list[PollOptionTally]

    @property
    def total_votes(self) -> int:
        return sum(len(o.voters) for o in self.options)


def _now() -> datetime.datetime:
    return datetime.datetime.now(datetime.UTC)


def create_poll(
    question: str,
    options: list[str],
    slack_channel_slack_id: str,
    creator_slack_user_slack_id: str,
    anonymous: bool = False,
    vote_limit: int | None = None,
    session: Session | None = None,
) -> PollSnapshot:
    """Insert a poll and its options in one transaction."""
    with SessionManager(session) as session:
        poll = Poll(
            question=question,
            slack_channel_slack_id=slack_channel_slack_id,
            creator_slack_user_slack_id=creator_slack_user_slack_id,
            anonymous=anonymous,
            vote_limit=vote_limit,
            created_at=_now(),
        )
        session.add(poll)
        session.flush()
        for position, text in enumerate(options):
            session.add(PollOption(poll_id=poll.id, position=position, text=text))
        session.commit()
        return _snapshot(session, poll.id)


def set_poll_message(
    poll_id: int,
    slack_channel_slack_id: str,
    slack_message_ts: str,
    session: Session | None = None,
) -> None:
    with SessionManager(session) as session:
        poll = session.get(Poll, poll_id)
        poll.slack_channel_slack_id = slack_channel_slack_id
        poll.slack_message_ts = slack_message_ts
        session.commit()


def get_poll_snapshot(
    poll_id: int, session: Session | None = None
) -> PollSnapshot | None:
    with SessionManager(session) as session:
        return _snapshot(session, poll_id)


def cast_poll_vote(
    poll_id: int,
    poll_option_id: int,
    slack_user_slack_id: str,
    session: Session | None = None,
) -> tuple[VoteOutcome, PollSnapshot | None]:
    """Toggle a person's vote on an option, enforcing the poll's vote limit.

    The poll row is locked so concurrent clicks on one poll are serialized.
    """
    with SessionManager(session) as session:
        poll = session.exec(
            select(Poll).where(Poll.id == poll_id).with_for_update()
        ).first()
        option = session.get(PollOption, poll_option_id)
        if poll is None or option is None or option.poll_id != poll.id:
            session.rollback()
            return VoteOutcome.NOT_FOUND, None
        if poll.closed_at is not None:
            session.rollback()
            return VoteOutcome.CLOSED, _snapshot(session, poll_id)

        active = list(
            session.exec(
                select(PollVote).where(
                    PollVote.poll_id == poll_id,
                    PollVote.slack_user_slack_id == slack_user_slack_id,
                    PollVote.removed_at.is_(None),
                )
            ).all()
        )
        now = _now()
        existing = next((v for v in active if v.poll_option_id == poll_option_id), None)

        if existing is not None:
            existing.removed_at = now
            outcome = VoteOutcome.REMOVED
        elif poll.vote_limit is not None and len(active) >= poll.vote_limit:
            if poll.vote_limit != 1:
                session.rollback()
                return VoteOutcome.LIMIT_REACHED, _snapshot(session, poll_id)
            for vote in active:
                vote.removed_at = now
            outcome = VoteOutcome.SWITCHED
        else:
            outcome = VoteOutcome.ADDED

        if outcome != VoteOutcome.REMOVED:
            # flush removals first so the partial unique index never sees two active rows
            session.flush()
            session.add(
                PollVote(
                    poll_id=poll_id,
                    poll_option_id=poll_option_id,
                    slack_user_slack_id=slack_user_slack_id,
                    created_at=now,
                )
            )
        session.commit()
        return outcome, _snapshot(session, poll_id)


def close_poll(poll_id: int, session: Session | None = None) -> PollSnapshot | None:
    with SessionManager(session) as session:
        poll = session.get(Poll, poll_id)
        if poll is None:
            return None
        if poll.closed_at is None:
            poll.closed_at = _now()
            session.commit()
        return _snapshot(session, poll_id)


def _snapshot(session: Session, poll_id: int) -> PollSnapshot | None:
    poll = session.get(Poll, poll_id)
    if poll is None:
        return None
    options = session.exec(
        select(PollOption)
        .where(PollOption.poll_id == poll_id)
        .order_by(PollOption.position)
    ).all()
    votes = session.exec(
        select(PollVote)
        .where(PollVote.poll_id == poll_id, PollVote.removed_at.is_(None))
        .order_by(PollVote.created_at, PollVote.id)
    ).all()
    tallies = {o.id: PollOptionTally(option=o) for o in options}
    for vote in votes:
        tallies[vote.poll_option_id].voters.append(vote.slack_user_slack_id)
    return PollSnapshot(poll=poll, options=list(tallies.values()))
