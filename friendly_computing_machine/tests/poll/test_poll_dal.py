import pytest
from sqlalchemy import event
from sqlalchemy.exc import IntegrityError
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db.dal.poll_dal import (
    VoteOutcome,
    cast_poll_vote,
    close_poll,
    create_poll,
    set_poll_message,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
    PollOption,
    PollVote,
)


@pytest.fixture
def session():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    tables = [Poll.__table__, PollOption.__table__, PollVote.__table__]
    Base.metadata.create_all(engine, tables=tables)
    with Session(engine) as s:
        yield s


def _poll(session, **kwargs):
    return create_poll(
        question="Lunch?",
        options=["Tacos", "Pizza", "Salad"],
        slack_channel_slack_id="C1",
        creator_slack_user_slack_id="U0",
        session=session,
        **kwargs,
    )


def _voters(snapshot):
    return [t.voters for t in snapshot.options]


def test_create_poll_orders_options(session):
    snap = _poll(session)
    assert [t.option.text for t in snap.options] == ["Tacos", "Pizza", "Salad"]
    assert [t.option.position for t in snap.options] == [0, 1, 2]
    assert snap.total_votes == 0

    set_poll_message(snap.poll.id, "C1", "123.456", session=session)
    assert session.get(Poll, snap.poll.id).slack_message_ts == "123.456"


def test_vote_toggle_keeps_history(session):
    snap = _poll(session)
    tacos = snap.options[0].option.id

    outcome, snap = cast_poll_vote(snap.poll.id, tacos, "U1", session=session)
    assert outcome == VoteOutcome.ADDED
    assert _voters(snap) == [["U1"], [], []]

    outcome, snap = cast_poll_vote(snap.poll.id, tacos, "U1", session=session)
    assert outcome == VoteOutcome.REMOVED
    assert snap.total_votes == 0

    outcome, snap = cast_poll_vote(snap.poll.id, tacos, "U1", session=session)
    assert outcome == VoteOutcome.ADDED
    rows = session.exec(select(PollVote)).all()
    assert len(rows) == 2
    assert sum(r.removed_at is None for r in rows) == 1


def test_unlimited_allows_multiple_options(session):
    snap = _poll(session)
    for tally in snap.options:
        outcome, snap = cast_poll_vote(
            snap.poll.id, tally.option.id, "U1", session=session
        )
        assert outcome == VoteOutcome.ADDED
    assert _voters(snap) == [["U1"], ["U1"], ["U1"]]


def test_limit_one_switches_vote(session):
    snap = _poll(session, vote_limit=1)
    tacos, pizza = snap.options[0].option.id, snap.options[1].option.id
    cast_poll_vote(snap.poll.id, tacos, "U1", session=session)
    outcome, snap = cast_poll_vote(snap.poll.id, pizza, "U1", session=session)
    assert outcome == VoteOutcome.SWITCHED
    assert _voters(snap) == [[], ["U1"], []]


def test_limit_n_rejects_extra_vote(session):
    snap = _poll(session, vote_limit=2)
    ids = [t.option.id for t in snap.options]
    cast_poll_vote(snap.poll.id, ids[0], "U1", session=session)
    cast_poll_vote(snap.poll.id, ids[1], "U1", session=session)
    outcome, snap = cast_poll_vote(snap.poll.id, ids[2], "U1", session=session)
    assert outcome == VoteOutcome.LIMIT_REACHED
    assert _voters(snap) == [["U1"], ["U1"], []]
    # other people are unaffected
    outcome, _ = cast_poll_vote(snap.poll.id, ids[2], "U2", session=session)
    assert outcome == VoteOutcome.ADDED


def test_closed_poll_rejects_votes(session):
    snap = _poll(session)
    tacos = snap.options[0].option.id
    cast_poll_vote(snap.poll.id, tacos, "U1", session=session)
    snap = close_poll(snap.poll.id, session=session)
    assert snap.poll.closed_at is not None
    outcome, snap = cast_poll_vote(snap.poll.id, tacos, "U2", session=session)
    assert outcome == VoteOutcome.CLOSED
    assert _voters(snap) == [["U1"], [], []]


def test_option_from_another_poll_is_not_found(session):
    first = _poll(session)
    second = _poll(session)
    outcome, snap = cast_poll_vote(
        first.poll.id, second.options[0].option.id, "U1", session=session
    )
    assert outcome == VoteOutcome.NOT_FOUND
    assert snap is None


def test_one_active_vote_per_option_enforced_by_index(session):
    snap = _poll(session)
    tacos = snap.options[0].option.id
    for _ in range(2):
        session.add(
            PollVote(
                poll_id=snap.poll.id, poll_option_id=tacos, slack_user_slack_id="U1"
            )
        )
    with pytest.raises(IntegrityError):
        session.commit()
